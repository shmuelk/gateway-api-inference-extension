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

	"sigs.k8s.io/controller-runtime/pkg/log"

	envoyhandlers "sigs.k8s.io/gateway-api-inference-extension/pkg/common/envoy/handlers"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
)

func (sh *ServerHandler) handleResponseBodyHelper(ctx context.Context, reqCtx *envoyhandlers.ExtProcRequestContext, responseBytes []byte) error {
	logger := log.FromContext(ctx)

	parsedResponse, parseErr := sh.parser.ParseResponse(ctx, responseBytes, reqCtx.Response.Headers, true)
	if parseErr != nil {
		logger.Error(parseErr, "response parsing")
	}
	if parsedResponse != nil && parsedResponse.Usage != nil {
		sh.reqCtx.Usage = *parsedResponse.Usage
		logger.V(logutil.VERBOSE).Info("Response generated", "usage", sh.reqCtx.Usage)
	}
	_, err := sh.director.HandleResponseBodyComplete(ctx, reqCtx, sh.reqCtx)
	return err
}

// The function is to handle streaming response if the modelServer is streaming.
func (sh *ServerHandler) HandleResponseBodyModelStreamingHelper(ctx context.Context, reqCtx *envoyhandlers.ExtProcRequestContext, responseBytes []byte, endOfStream bool) {
	logger := log.FromContext(ctx)
	_, err := sh.director.HandleResponseBodyStreaming(ctx, reqCtx, sh.reqCtx)
	if err != nil {
		logger.Error(err, "error in HandleResponseBodyStreaming")
	}
	parsedResp, err := sh.parser.ParseResponse(ctx, responseBytes, reqCtx.Response.Headers, endOfStream)
	if err != nil {
		logger.Error(err, "streaming response parsing")
	} else if parsedResp != nil && parsedResp.Usage != nil {
		sh.reqCtx.Usage = *parsedResp.Usage
		metrics.RecordInputTokens(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, sh.reqCtx.Usage.PromptTokens)
		metrics.RecordOutputTokens(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, sh.reqCtx.Usage.CompletionTokens)
		if sh.reqCtx.Usage.PromptTokenDetails != nil {
			metrics.RecordPromptCachedTokens(sh.reqCtx.IncomingModelName, sh.reqCtx.TargetModelName, sh.reqCtx.Usage.PromptTokenDetails.CachedTokens)
		}
	}
}
