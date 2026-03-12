/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package handlers

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	envoyhandlers "sigs.k8s.io/gateway-api-inference-extension/pkg/common/envoy/handlers"
	errcommon "sigs.k8s.io/gateway-api-inference-extension/pkg/common/error"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	fwkrq "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	fwkrh "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requesthandling"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metadata"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
)

type Director interface {
	HandleRequest(ctx context.Context, extProcReqCtx *envoyhandlers.ExtProcRequestContext, reqCtx *RequestContext) error
	HandleResponseReceived(ctx context.Context, extProcReqCtx *envoyhandlers.ExtProcRequestContext, reqCtx *RequestContext) (*RequestContext, error)
	HandleResponseBodyStreaming(ctx context.Context, extProcReqCtx *envoyhandlers.ExtProcRequestContext, reqCtx *RequestContext) (*RequestContext, error)
	HandleResponseBodyComplete(ctx context.Context, extProcReqCtx *envoyhandlers.ExtProcRequestContext, reqCtx *RequestContext) (*RequestContext, error)
	GetRandomEndpoint() *fwkdl.EndpointMetadata
}

type Datastore interface {
	PoolGet() (*datalayer.EndpointPool, error)
}

// RequestContext stores context information during the life time of an HTTP request.
type RequestContext struct {
	TargetPod         *fwkdl.EndpointMetadata
	TargetEndpoint    string
	IncomingModelName string
	TargetModelName   string
	FairnessID        string
	ObjectiveKey      string
	Usage             fwkrq.Usage
	SchedulingRequest *schedulingtypes.LLMRequest
}

// ServerHandlerFactory is the factory for the EPP specific logic of the Envoy external processing server.
type ServerHandlerFactory struct {
	datastore Datastore
	director  Director
	parser    fwkrh.Parser
}

func NewServerHandlerFactory(datastore Datastore, director Director, parser fwkrh.Parser) *ServerHandlerFactory {
	return &ServerHandlerFactory{
		director:  director,
		datastore: datastore,
		parser:    parser,
	}
}

func (shf *ServerHandlerFactory) CreateHandler(logger logr.Logger) envoyhandlers.Handler {
	return &ServerHandler{
		datastore: shf.datastore,
		director:  shf.director,
		parser:    shf.parser,
		reqCtx:    &RequestContext{},
		logger:    logger,
	}
}

type ServerHandler struct {
	datastore Datastore
	director  Director
	parser    fwkrh.Parser
	reqCtx    *RequestContext
	logger    logr.Logger
}

func (sh *ServerHandler) HandleRequestHeaders(reqCtx *envoyhandlers.ExtProcRequestContext, endOfStream bool) error {
	// an EoS in the request headers means this request has no body or trailers.
	if endOfStream {
		// We will route this request to a random endpoint as this is assumed to just be a GET
		// More context: https://github.com/kubernetes-sigs/gateway-api-inference-extension/pull/526
		// The above PR will address endpoint admission, but currently any request without a body will be
		// routed to a random upstream endpoint.
		endpoint := sh.director.GetRandomEndpoint()
		if endpoint == nil {
			return errcommon.Error{Code: errcommon.Internal, Msg: "no pods available in datastore"}
		}
		sh.reqCtx.TargetEndpoint = endpoint.GetIPAddress() + ":" + endpoint.GetPort()
		reqCtx.Request.DynamicMetadata = sh.generateMetadata(sh.reqCtx.TargetEndpoint)

		reqCtx.Request.AddedHeaders[metadata.DestinationEndpointKey] = sh.reqCtx.TargetEndpoint

		return nil
	}

	for key, value := range reqCtx.Request.Headers {
		switch key {
		case metadata.FlowFairnessIDKey:
			sh.reqCtx.FairnessID = value
		case metadata.ObjectiveKey:
			sh.reqCtx.ObjectiveKey = value
		case metadata.ModelNameRewriteKey:
			sh.reqCtx.TargetModelName = value
		}
	}

	if sh.reqCtx.FairnessID == "" {
		sh.reqCtx.FairnessID = metadata.DefaultFairnessID
	}

	return nil
}

func (sh *ServerHandler) HandleRequest(ctx context.Context, reqCtx *envoyhandlers.ExtProcRequestContext) error {
	err := sh.director.HandleRequest(ctx, reqCtx, sh.reqCtx)

	if len(reqCtx.Request.AddedHeaders[metadata.DestinationEndpointKey]) == 0 {
		reqCtx.Request.AddedHeaders[metadata.DestinationEndpointKey] = sh.reqCtx.TargetEndpoint
		reqCtx.Request.DynamicMetadata = sh.generateMetadata(sh.reqCtx.TargetEndpoint)
	}

	if err == nil {
		metrics.RecordRequestCounter(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName)
		metrics.RecordRequestSizes(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, reqCtx.RequestSize)
	}

	return err
}

func (sh *ServerHandler) HandleResponseReceived(ctx context.Context, reqCtx *envoyhandlers.ExtProcRequestContext) error {
	_, err := sh.director.HandleResponseReceived(ctx, reqCtx, sh.reqCtx)
	return err
}

func (sh *ServerHandler) HandleResponseBody(ctx context.Context, reqCtx *envoyhandlers.ExtProcRequestContext, responseBytes []byte) error {
	err := sh.handleResponseBodyHelper(ctx, reqCtx, responseBytes)
	if err != nil {
		return err
	}
	metrics.RecordRequestLatencies(ctx, sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, reqCtx.RequestReceivedTimestamp, reqCtx.ResponseCompleteTimestamp)
	metrics.RecordResponseSizes(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, reqCtx.ResponseSize)
	metrics.RecordInputTokens(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, sh.reqCtx.Usage.PromptTokens)
	metrics.RecordOutputTokens(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, sh.reqCtx.Usage.CompletionTokens)
	if sh.reqCtx.Usage.PromptTokenDetails != nil {
		metrics.RecordPromptCachedTokens(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, sh.reqCtx.Usage.PromptTokenDetails.CachedTokens)
	}

	return nil
}

func (sh *ServerHandler) HandleResponseBodyModelStreaming(ctx context.Context, reqCtx *envoyhandlers.ExtProcRequestContext, responseBytes []byte, endOfStream bool) {
	sh.HandleResponseBodyModelStreamingHelper(ctx, reqCtx, responseBytes, endOfStream)
}

func (sh *ServerHandler) HandleResponseBodyModelStreamingComplete(ctx context.Context, reqCtx *envoyhandlers.ExtProcRequestContext) {
	if _, err := sh.director.HandleResponseBodyComplete(ctx, reqCtx, sh.reqCtx); err != nil {
		log.FromContext(ctx).Error(err, "error in HandleResponseBodyComplete")
	}
	metrics.RecordRequestLatencies(ctx, sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, reqCtx.RequestReceivedTimestamp, reqCtx.ResponseCompleteTimestamp)
	metrics.RecordResponseSizes(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, reqCtx.ResponseSize)
	metrics.RecordNormalizedTimePerOutputToken(ctx, sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, reqCtx.RequestReceivedTimestamp, reqCtx.ResponseCompleteTimestamp, sh.reqCtx.Usage.CompletionTokens)
}

func (sh *ServerHandler) ResponseSent(_ *envoyhandlers.ExtProcRequestContext) {
	sh.logger.V(1).Info("EPP sent request body response(s) to proxy", "modelName", sh.reqCtx.IncomingModelName, "targetModelName", sh.reqCtx.TargetModelName)
	metrics.IncRunningRequests(sh.reqCtx.IncomingModelName)
}

func (sh *ServerHandler) RequestEnded(err error, reqCtx *envoyhandlers.ExtProcRequestContext) {
	if reqCtx.ResponseStatusCode != "" {
		metrics.RecordRequestErrCounter(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, reqCtx.ResponseStatusCode)
	} else if err != nil {
		metrics.RecordRequestErrCounter(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, errcommon.CanonicalCode(err))
	}
	if reqCtx.RequestRunning {
		metrics.DecRunningRequests(sh.reqCtx.IncomingModelName)
	}

	// If we scheduled a pod (TargetPod != nil) but never marked the response  as complete (e.g. error, disconnect,
	// panic), force the completion hooks to run.
	if sh.reqCtx.TargetPod != nil && !reqCtx.ResponseComplete {
		// Use a fresh context as the request context might be canceled (Client Disconnect).
		// We only need logging from the original context.
		cleanupCtx := log.IntoContext(context.Background(), sh.logger)
		if _, err := sh.director.HandleResponseBodyComplete(cleanupCtx, reqCtx, sh.reqCtx); err != nil {
			sh.logger.Error(err, "error in HandleResponseBodyComplete")
		}
	}
}

func (sh *ServerHandler) IsSystemOwnedHeader(key string) bool {
	return request.IsSystemOwnedHeader(key)
}

func (sh *ServerHandler) SetLogger(logger logr.Logger) {
	sh.logger = logger
}
