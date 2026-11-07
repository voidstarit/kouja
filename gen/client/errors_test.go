package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func bddTestErrorRetryability(t testing.TB) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{
			"code": 14,
			"message": "backend unavailable",
			"details": [{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo",
				"reason": "UNAVAILABLE",
				"domain": "kouja.v1",
				"metadata": {"retryable": "true", "operation_id": "op-1"}
			}]
		}`))
	}))
	defer server.Close()

	client, err := NewHTTP(server.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	_, err = client.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	var httpErr *Error
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if httpErr.HTTPStatus != http.StatusServiceUnavailable || httpErr.Code != codes.Unavailable {
		t.Fatalf("error = %#v", httpErr)
	}
	if httpErr.Reason != "UNAVAILABLE" || httpErr.OperationID != "op-1" || !httpErr.Retryable {
		t.Fatalf("error metadata = %#v", httpErr)
	}
	if !httpErr.IsRetryable() || !IsRetryable(err) {
		t.Fatal("retryable error was not reported as retryable")
	}
	info, ok := ErrorInfo(err)
	if !ok || info.GetReason() != "UNAVAILABLE" || info.GetMetadata()["operation_id"] != "op-1" {
		t.Fatalf("ErrorInfo = %#v, %t", info, ok)
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("status.Code = %v", status.Code(err))
	}
	found := false
	for _, detail := range status.Convert(err).Details() {
		if _, ok := detail.(*errdetails.ErrorInfo); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("status details did not preserve ErrorInfo")
	}

	nonRetryableServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{
			"code": 14,
			"message": "guarded unavailable",
			"details": [{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo",
				"reason": "UNAVAILABLE",
				"domain": "kouja.v1",
				"metadata": {"retryable": "false"}
			}]
		}`))
	}))
	defer nonRetryableServer.Close()
	nonRetryableClient, err := NewHTTP(nonRetryableServer.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	_, err = nonRetryableClient.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if httpErr.Retryable || httpErr.IsRetryable() || IsRetryable(err) {
		t.Fatal("server retryable=false was ignored")
	}

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"code":8,"message":"capacity"}`))
	}))
	defer fallbackServer.Close()
	fallbackClient, err := NewHTTP(fallbackServer.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	_, err = fallbackClient.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if !httpErr.IsRetryable() || !IsRetryable(err) {
		t.Fatal("ResourceExhausted fallback was not retryable")
	}

	retryableStatus, err := status.New(codes.Unavailable, "backend").WithDetails(&errdetails.ErrorInfo{
		Reason:   "UNAVAILABLE",
		Domain:   "kouja.v1",
		Metadata: map[string]string{"retryable": "true"},
	})
	if err != nil {
		t.Fatalf("status.WithDetails: %v", err)
	}
	if !IsRetryable(retryableStatus.Err()) {
		t.Fatal("gRPC retryable ErrorInfo was not retryable")
	}
	if info, ok := ErrorInfo(retryableStatus.Err()); !ok || info.GetReason() != "UNAVAILABLE" {
		t.Fatalf("gRPC ErrorInfo = %#v, %t", info, ok)
	}

	guardedStatus, err := status.New(codes.Unavailable, "guarded").WithDetails(&errdetails.ErrorInfo{
		Reason:   "UNAVAILABLE",
		Domain:   "kouja.v1",
		Metadata: map[string]string{"retryable": "false"},
	})
	if err != nil {
		t.Fatalf("status.WithDetails: %v", err)
	}
	if IsRetryable(guardedStatus.Err()) {
		t.Fatal("gRPC retryable=false was ignored")
	}
	if !IsRetryable(status.Error(codes.Unavailable, "transport")) {
		t.Fatal("Unavailable fallback was not retryable")
	}
	if IsRetryable(status.Error(codes.FailedPrecondition, "session")) {
		t.Fatal("FailedPrecondition was retryable")
	}
	if IsRetryable(errors.New("network")) {
		t.Fatal("unclassified transport error was retryable")
	}
	if IsRetryable(nil) {
		t.Fatal("nil error was retryable")
	}
	var nilErr *Error
	if nilErr.IsRetryable() {
		t.Fatal("nil *Error was retryable")
	}
}
