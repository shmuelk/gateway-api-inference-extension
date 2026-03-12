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
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	envoyTypePb "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	errcommon "sigs.k8s.io/gateway-api-inference-extension/pkg/common/error"
	"sigs.k8s.io/gateway-api-inference-extension/test/utils"
)

func TestServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	serverHandlerFactory := testServerHandlerFactory{}
	server := NewServer(&serverHandlerFactory, "", "")
	testListener, errChan := utils.SetupTestProcessServer(t, ctx, nil, server)

	// test with request ID header sent and non-streaming response
	serverHandler = &testServerHandler{returnError: returnNoError}
	testServerHelper(ctx, t, true, false)

	// test with request ID header generated and non-streaming response
	serverHandler.reset()
	testServerHelper(ctx, t, false, false)

	// test with request ID header generated and streaming response
	serverHandler.reset()
	testServerHelper(ctx, t, false, true)

	// test with a simple error returned by the ServerHandler's HandleRequestHeaders function
	// request ID header generated and non-streaming response
	serverHandler = &testServerHandler{returnError: returnSimpleError}
	testServerHelper(ctx, t, false, false)

	// test with a BadRequest error returned by the ServerHandler's HandleRequestHeaders function
	// request ID header generated and non-streaming response
	serverHandler = &testServerHandler{returnError: returnBadRequest}
	testServerHelper(ctx, t, false, false)

	cancel()
	<-errChan
	testListener.Close()
}

func testServerHelper(ctx context.Context, t *testing.T, addRequestID bool, streamingResponse bool) {
	process, conn := utils.GetProcessServerClient(ctx, t)
	defer conn.Close()

	// Send request headers - no response expected
	headersToSend := map[string]string{
		"x-test":  "body",
		":method": "POST",
	}
	if addRequestID {
		headersToSend["x-request-id"] = "test-request-id"
	}
	headers := utils.BuildEnvoyGRPCHeaders(headersToSend, true)
	request := &pb.ProcessingRequest{
		Request: &pb.ProcessingRequest_RequestHeaders{
			RequestHeaders: headers,
		},
	}
	err := process.Send(request)
	if err != nil {
		t.Error("Error sending request headers", err)
	}

	if serverHandler.returnError != returnNoError {
		errorResponse, err := process.Recv()
		switch serverHandler.returnError {
		case returnSimpleError:
			if err == nil {
				t.Error("Failed to get an error")
			}
			if grpcStatus, ok := status.FromError(err); !ok ||
				grpcStatus.Code() != codes.Unknown || grpcStatus.Message() != "failed to handle request: a fake error for testing" {
				t.Error("Received wrong type of error")
			}
		case returnBadRequest:
			if err != nil {
				t.Error("Error receiving error response", err)
			}
			if errorResponse == nil || errorResponse.GetImmediateResponse() == nil ||
				errorResponse.GetImmediateResponse().GetStatus() == nil ||
				errorResponse.GetImmediateResponse().GetStatus().Code != envoyTypePb.StatusCode_BadRequest {
				t.Error("Received the wrong message")
			}
		}
		return
	}

	// Send request body
	requestBody := "{\"model\":\"food-review\",\"prompt\":\"Is banana tasty?\"}"
	request = &pb.ProcessingRequest{
		Request: &pb.ProcessingRequest_RequestBody{
			RequestBody: &pb.HttpBody{
				Body:        []byte(requestBody),
				EndOfStream: true,
			},
		},
	}
	err = process.Send(request)
	if err != nil {
		t.Error("Error sending request body", err)
	}

	// Receive request headers and check
	responseReqHeaders, err := process.Recv()
	if err != nil {
		t.Error("Error receiving response", err)
	}

	if serverHandler.reqCtx == nil {
		t.Error("reqCtx is nil")
	}
	if !serverHandler.handleRequestHeadersCalled {
		t.Error("Didn't call handler's HandleRequestHeaders function")
	}
	if requestID, ok := serverHandler.reqCtx.Request.Headers["x-request-id"]; ok {
		if addRequestID && requestID != "test-request-id" {
			t.Error("request ID overwritten")
		}
	} else {
		t.Error("request ID missing")
	}

	if !serverHandler.handleRequestCalled {
		t.Error("Didn't call handler's HandleRequest function")
	}
	if responseReqHeaders == nil || responseReqHeaders.GetRequestHeaders() == nil ||
		responseReqHeaders.GetRequestHeaders().Response == nil ||
		responseReqHeaders.GetRequestHeaders().Response.HeaderMutation == nil ||
		responseReqHeaders.GetRequestHeaders().Response.HeaderMutation.SetHeaders == nil {
		t.Error("Invalid request headers response")
	}

	// Receive request body and check
	responseReqBody, err := process.Recv()
	if err != nil {
		t.Error("Error receiving response", err)
	} else if responseReqBody == nil || responseReqBody.GetRequestBody() == nil ||
		responseReqBody.GetRequestBody().Response == nil ||
		responseReqBody.GetRequestBody().Response.BodyMutation == nil ||
		responseReqBody.GetRequestBody().Response.BodyMutation.GetStreamedResponse() == nil {
		t.Error("Invalid request body response")
	}

	// Send response headers
	responseHeaders := map[string]string{
		"x-request-id": "test-request-id",
	}
	if streamingResponse {
		responseHeaders["content-type"] = "text/event-stream"
	}
	headers = utils.BuildEnvoyGRPCHeaders(responseHeaders, true)
	response := &pb.ProcessingRequest{
		Request: &pb.ProcessingRequest_ResponseHeaders{
			ResponseHeaders: headers,
		},
	}
	err = process.Send(response)
	if err != nil {
		t.Error("Error sending request headers", err)
	}

	// Receive response headers and check
	responseHeadersResponse, err := process.Recv()
	if err != nil {
		t.Error("Error receiving response", err)
	}

	if !serverHandler.handleResponseReceivedCalled {
		t.Error("Didn't call handler's HandleResponseReceived function")
	}

	if responseHeadersResponse == nil || responseHeadersResponse.GetResponseHeaders() == nil ||
		responseHeadersResponse.GetResponseHeaders().Response == nil ||
		responseHeadersResponse.GetResponseHeaders().Response.HeaderMutation == nil ||
		responseHeadersResponse.GetResponseHeaders().Response.HeaderMutation.SetHeaders == nil {
		t.Error("Invalid response headers response", responseHeadersResponse)
	}
	foundHeader := false
	for _, header := range responseHeadersResponse.GetResponseHeaders().Response.HeaderMutation.SetHeaders {
		if header.Header.Key == "x-went-into-resp-headers" {
			foundHeader = true
			break
		}
	}
	if !foundHeader {
		t.Error("failed to find x-went-into-resp-headers header")
	}

	// Send response body
	responseBody := "{\"id\":\"cmpl-45719699-f4cf-598c-9796-00ba9228bb74\",\"created\":1773220653,\"model\":\"food-review\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":3,\"total_tokens\":4},\"object\":\"text_completion\",\"kv_transfer_params\":null,\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"text\":\"To be or \"}]}"
	request = &pb.ProcessingRequest{
		Request: &pb.ProcessingRequest_ResponseBody{
			ResponseBody: &pb.HttpBody{
				Body:        []byte(responseBody),
				EndOfStream: true,
			},
		},
	}
	err = process.Send(request)
	if err != nil {
		t.Error("Error sending response body", err)
	}

	// Receive response headers and check
	responseBodyResponse, err := process.Recv()
	if err != nil {
		t.Error("Error receiving response", err)
	}
	if responseBodyResponse == nil || responseBodyResponse.GetResponseBody() == nil ||
		responseBodyResponse.GetResponseBody().GetResponse() == nil ||
		responseBodyResponse.GetResponseBody().GetResponse().GetBodyMutation() == nil ||
		responseBodyResponse.GetResponseBody().GetResponse().GetBodyMutation().GetStreamedResponse() == nil ||
		!bytes.Equal(responseBodyResponse.GetResponseBody().GetResponse().GetBodyMutation().GetStreamedResponse().GetBody(), []byte(responseBody)) {
		t.Error("Received an incorrect responseBodyResponse message")
	}
	if serverHandler.reqCtx.modelServerStreaming != streamingResponse {
		if streamingResponse {
			t.Error("server did not determine that the response was streamed")
		}
		t.Error("the server thought the response was streamed when it wasn't")
	}
}

var serverHandler *testServerHandler

type testServerHandlerFactory struct{}

func (tshf *testServerHandlerFactory) CreateHandler(logger logr.Logger) Handler {
	return serverHandler
}

const (
	returnNoError     = 0
	returnSimpleError = 1
	returnBadRequest  = 2
)

type testServerHandler struct {
	returnError                  int
	reqCtx                       *ExtProcRequestContext
	handleRequestHeadersCalled   bool
	handleRequestCalled          bool
	handleResponseReceivedCalled bool
}

func (tsh *testServerHandler) HandleRequestHeaders(reqCtx *ExtProcRequestContext, endOfStream bool) error {
	tsh.reqCtx = reqCtx
	tsh.handleRequestHeadersCalled = true
	switch tsh.returnError {
	case returnSimpleError:
		return errors.New("a fake error for testing")
	case returnBadRequest:
		return errcommon.Error{Code: errcommon.BadRequest}
	}
	return nil
}

func (tsh *testServerHandler) HandleRequest(ctx context.Context, reqCtx *ExtProcRequestContext) error {
	tsh.handleRequestCalled = true
	return nil
}

func (tsh *testServerHandler) HandleResponseReceived(ctx context.Context, reqCtx *ExtProcRequestContext) error {
	tsh.handleResponseReceivedCalled = true
	return nil
}

func (tsh *testServerHandler) HandleResponseBody(ctx context.Context, reqCtx *ExtProcRequestContext, responseBytes []byte) error {
	return nil
}

func (tsh *testServerHandler) HandleResponseBodyModelStreaming(ctx context.Context, reqCtx *ExtProcRequestContext, responseBytes []byte, endOfStream bool) {
}

func (tsh *testServerHandler) HandleResponseBodyModelStreamingComplete(ctx context.Context, reqCtx *ExtProcRequestContext) {
}

func (tsh *testServerHandler) ResponseSent(reqCtx *ExtProcRequestContext) {
}

func (tsh *testServerHandler) RequestEnded(err error, reqCtx *ExtProcRequestContext) {
}

func (tsh *testServerHandler) IsSystemOwnedHeader(key string) bool {
	return false
}

func (tsh *testServerHandler) SetLogger(logger logr.Logger) {}

func (tsh *testServerHandler) reset() {
	tsh.returnError = returnNoError
	tsh.handleRequestCalled = false
	tsh.handleRequestHeadersCalled = false
	tsh.handleResponseReceivedCalled = false
	tsh.reqCtx = nil
}
