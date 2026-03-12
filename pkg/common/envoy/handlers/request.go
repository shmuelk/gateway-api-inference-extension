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
	"maps"
	"strconv"

	configPb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/protobuf/types/known/structpb"
)

// GenerateRequestBodyResponses splits the request body bytes into chunked body
// responses and wraps each chunk in a ProcessingResponse_RequestBody envelope.
func GenerateRequestBodyResponses(requestBodyBytes []byte) []*extProcPb.ProcessingResponse {
	commonResponses := BuildChunkedBodyResponses(requestBodyBytes, true)
	responses := make([]*extProcPb.ProcessingResponse, 0, len(commonResponses))
	for _, commonResp := range commonResponses {
		resp := &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_RequestBody{
				RequestBody: &extProcPb.BodyResponse{
					Response: commonResp,
				},
			},
		}
		responses = append(responses, resp)
	}
	return responses
}

func (s *Server) generateRequestHeaderResponse(ctx context.Context, reqCtx *ExtProcRequestContext, requestSize int) *extProcPb.ProcessingResponse {
	// The Endpoint Picker supports two approaches to communicating the target endpoint, as a request header
	// and as an unstructure ext-proc response metadata key/value pair. This enables different integration
	// options for gateway providers.
	if reqCtx.Response.DynamicMetadata != nil {
		if reqCtx.Request.DynamicMetadata.Fields == nil {
			reqCtx.Request.DynamicMetadata.Fields = make(map[string]*structpb.Value)
		}
		maps.Copy(reqCtx.Request.DynamicMetadata.Fields, reqCtx.Response.DynamicMetadata.Fields)
	}

	return &extProcPb.ProcessingResponse{
		Response: &extProcPb.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extProcPb.HeadersResponse{
				Response: &extProcPb.CommonResponse{
					ClearRouteCache: true,
					HeaderMutation: &extProcPb.HeaderMutation{
						SetHeaders: s.generateHeaders(ctx, reqCtx, requestSize),
					},
				},
			},
		},
		DynamicMetadata: reqCtx.Request.DynamicMetadata,
	}
}

func (s *Server) generateHeaders(ctx context.Context, reqCtx *ExtProcRequestContext, requestSize int) []*configPb.HeaderValueOption {
	// can likely refactor these two bespoke headers to be updated in PostDispatch, to centralize logic.
	headers := []*configPb.HeaderValueOption{}

	for key, value := range reqCtx.Request.AddedHeaders {
		headers = append(headers, &configPb.HeaderValueOption{
			Header: &configPb.HeaderValue{
				Key:      key,
				RawValue: []byte(value),
			},
		})
	}
	if requestSize > 0 {
		// We need to update the content length header if the body is mutated, see Envoy doc:
		// https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/http/ext_proc/v3/processing_mode.proto
		headers = append(headers, &configPb.HeaderValueOption{
			Header: &configPb.HeaderValue{
				Key:      "Content-Length",
				RawValue: []byte(strconv.Itoa(requestSize)),
			},
		})
	}

	// Inject trace context headers for propagation to downstream services
	traceHeaders := make(map[string]string)
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(ctx, propagation.MapCarrier(traceHeaders))
	for key, value := range traceHeaders {
		headers = append(headers, &configPb.HeaderValueOption{
			Header: &configPb.HeaderValue{
				Key:      key,
				RawValue: []byte(value),
			},
		})
	}

	// Include any non-system-owned headers.
	for key, value := range reqCtx.Request.Headers {
		if reqCtx.handler.IsSystemOwnedHeader(key) {
			continue
		}
		headers = append(headers, &configPb.HeaderValueOption{
			Header: &configPb.HeaderValue{
				Key:      key,
				RawValue: []byte(value),
			},
		})
	}
	return headers
}
