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

	configPb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	reqenvoy "sigs.k8s.io/gateway-api-inference-extension/pkg/common/envoy/request"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
)

func (s *Server) handleResponseHeaders(ctx context.Context, reqCtx *ExtProcRequestContext, resp *extProcPb.ProcessingRequest_ResponseHeaders) error {
	for _, header := range resp.ResponseHeaders.Headers.Headers {
		reqCtx.Response.Headers[header.Key] = reqenvoy.GetHeaderValue(header)
	}

	return reqCtx.handler.HandleResponseReceived(ctx, reqCtx)
}

func (s *Server) generateResponseHeaderResponse(reqCtx *ExtProcRequestContext) *extProcPb.ProcessingResponse {
	return &extProcPb.ProcessingResponse{
		Response: &extProcPb.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &extProcPb.HeadersResponse{
				Response: &extProcPb.CommonResponse{
					HeaderMutation: &extProcPb.HeaderMutation{
						SetHeaders: generateResponseHeaders(reqCtx),
					},
				},
			},
		},
	}
}

func generateResponseBodyResponses(responseBodyBytes []byte, setEoS bool) []*extProcPb.ProcessingResponse {
	commonResponses := BuildChunkedBodyResponses(responseBodyBytes, setEoS)
	responses := make([]*extProcPb.ProcessingResponse, 0, len(commonResponses))
	for _, commonResp := range commonResponses {
		resp := &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_ResponseBody{
				ResponseBody: &extProcPb.BodyResponse{
					Response: commonResp,
				},
			},
		}
		responses = append(responses, resp)
	}
	return responses
}

func generateResponseHeaders(reqCtx *ExtProcRequestContext) []*configPb.HeaderValueOption {
	// can likely refactor these two bespoke headers to be updated in PostDispatch, to centralize logic.
	headers := []*configPb.HeaderValueOption{
		{
			Header: &configPb.HeaderValue{
				// This is for debugging purpose only.
				Key:      "x-went-into-resp-headers",
				RawValue: []byte("true"),
			},
		},
	}

	// Include any non-system-owned headers.
	for key, value := range reqCtx.Response.Headers {
		if request.IsSystemOwnedHeader(key) {
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

// AddStreamedResponseBody splits responseBodyBytes into chunked body responses
// and appends them as ResponseBody ProcessingResponses, mirroring
// GenerateRequestBodyResponses for the request path.
func AddStreamedResponseBody(responses []*extProcPb.ProcessingResponse, responseBodyBytes []byte) []*extProcPb.ProcessingResponse {
	commonResponses := BuildChunkedBodyResponses(responseBodyBytes, true)
	for _, commonResp := range commonResponses {
		responses = append(responses, &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_ResponseBody{
				ResponseBody: &extProcPb.BodyResponse{
					Response: commonResp,
				},
			},
		})
	}
	return responses
}
