package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordedHTTPRequest struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   []byte
}

type httpCall struct {
	name     string
	method   string
	path     string
	query    url.Values
	wantBody map[string]any
	invoke   func(context.Context, *HTTP) error
}

func bddTestHTTPClientCoverage(t testing.TB) {
	calls := []httpCall{
		{
			name: "CreateVolume", method: http.MethodPost, path: "/v1/volumes",
			wantBody: map[string]any{"volumeId": "vol-1", "sizeBytes": "1024", "vg": "vg-1"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.CreateVolume(ctx, &koujav1.CreateVolumeRequest{VolumeId: "vol-1", SizeBytes: 1024, Vg: "vg-1"})
				return err
			},
		},
		{
			name: "ExportVolume", method: http.MethodPost, path: "/v1/volumes/vol-1:export",
			wantBody: map[string]any{"initiator": "iqn.test", "protocol": "PROTOCOL_ISCSI"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ExportVolume(ctx, &koujav1.ExportVolumeRequest{VolumeId: "vol-1", Initiator: "iqn.test", Protocol: koujav1.Protocol_PROTOCOL_ISCSI})
				return err
			},
		},
		{
			name: "UnexportVolume", method: http.MethodPost, path: "/v1/volumes/vol-1:unexport",
			wantBody: map[string]any{"force": true},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.UnexportVolume(ctx, &koujav1.UnexportVolumeRequest{VolumeId: "vol-1", Force: true})
				return err
			},
		},
		{
			name: "ResizeVolume", method: http.MethodPost, path: "/v1/volumes/vol-1:resize",
			wantBody: map[string]any{"sizeBytes": "2048", "allowShrink": true},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ResizeVolume(ctx, &koujav1.ResizeVolumeRequest{VolumeId: "vol-1", SizeBytes: 2048, AllowShrink: true})
				return err
			},
		},
		{
			name: "DeleteVolume", method: http.MethodDelete, path: "/v1/volumes/vol-1",
			query: url.Values{"force": {"true"}, "requestId": {"req-1"}},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.DeleteVolume(ctx, &koujav1.DeleteVolumeRequest{VolumeId: "vol-1", Force: true, RequestId: "req-1"})
				return err
			},
		},
		{
			name: "GetVolume", method: http.MethodGet, path: "/v1/volumes/vol-1",
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.GetVolume(ctx, &koujav1.GetVolumeRequest{VolumeId: "vol-1"})
				return err
			},
		},
		{
			name: "ListVolumes", method: http.MethodGet, path: "/v1/volumes",
			query: url.Values{"pageSize": {"10"}, "pageToken": {"next"}},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ListVolumes(ctx, &koujav1.ListVolumesRequest{PageSize: 10, PageToken: "next"})
				return err
			},
		},
		{
			name: "GetCredential", method: http.MethodGet, path: "/v1/volumes/vol-1/credential",
			query: url.Values{"owner": {"alice"}},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.GetCredential(ctx, &koujav1.GetCredentialRequest{VolumeId: "vol-1", Owner: "alice"})
				return err
			},
		},
		{
			name: "RotateCredential", method: http.MethodPost, path: "/v1/volumes/vol-1:rotate-credential",
			wantBody: map[string]any{"requestId": "req-1"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.RotateCredential(ctx, &koujav1.RotateCredentialRequest{VolumeId: "vol-1", RequestId: "req-1"})
				return err
			},
		},
		{
			name: "GetCapacity", method: http.MethodGet, path: "/v1/capacity",
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.GetCapacity(ctx, &koujav1.GetCapacityRequest{})
				return err
			},
		},
		{
			name: "ListStorageClasses", method: http.MethodGet, path: "/v1/storage-classes",
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ListStorageClasses(ctx, &koujav1.ListStorageClassesRequest{})
				return err
			},
		},
		{
			name: "GetInfo", method: http.MethodGet, path: "/v1/info",
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.GetInfo(ctx, &koujav1.GetInfoRequest{})
				return err
			},
		},
		{
			name: "RegisterVG", method: http.MethodPost, path: "/v1/admin/vgs",
			wantBody: map[string]any{"vgName": "vg-1"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.RegisterVG(ctx, &koujav1.RegisterVGRequest{VgName: "vg-1"})
				return err
			},
		},
		{
			name: "DeregisterVG", method: http.MethodDelete, path: "/v1/admin/vgs/vg-1",
			query: url.Values{"requestId": {"req-1"}},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.DeregisterVG(ctx, &koujav1.DeregisterVGRequest{VgName: "vg-1", RequestId: "req-1"})
				return err
			},
		},
		{
			name: "ListVGs", method: http.MethodGet, path: "/v1/admin/vgs",
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ListVGs(ctx, &koujav1.ListVGsRequest{})
				return err
			},
		},
		{
			name: "ImportVolume", method: http.MethodPost, path: "/v1/admin/import",
			wantBody: map[string]any{"volumeId": "vol-1", "lvName": "lv-1"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ImportVolume(ctx, &koujav1.ImportVolumeRequest{VolumeId: "vol-1", LvName: "lv-1"})
				return err
			},
		},
		{
			name: "ResolveDrift", method: http.MethodPost, path: "/v1/admin/drift/vol-1:resolve",
			wantBody: map[string]any{"action": "RESOLVE_ACTION_RETRY", "reason": "test"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ResolveDrift(ctx, &koujav1.ResolveDriftRequest{VolumeId: "vol-1", Action: koujav1.ResolveAction_RESOLVE_ACTION_RETRY, Reason: "test"})
				return err
			},
		},
		{
			name: "ListOrphans", method: http.MethodGet, path: "/v1/admin/orphans",
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.ListOrphans(ctx, &koujav1.ListOrphansRequest{})
				return err
			},
		},
		{
			name: "WipeVolume", method: http.MethodPost, path: "/v1/admin/volumes/vol-1:wipe",
			wantBody: map[string]any{"acknowledgeDataLoss": true},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.WipeVolume(ctx, &koujav1.WipeVolumeRequest{VolumeId: "vol-1", AcknowledgeDataLoss: true})
				return err
			},
		},
		{
			name: "TransferOwnership", method: http.MethodPost, path: "/v1/admin/ownership:transfer",
			wantBody: map[string]any{"newOwner": "bob"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.TransferOwnership(ctx, &koujav1.TransferOwnershipRequest{VolumeId: "vol-1", NewOwner: "bob"})
				return err
			},
		},
		{
			name: "AdminDeleteVolume", method: http.MethodDelete, path: "/v1/admin/volumes/vol-1",
			query: url.Values{"acknowledgeDataLoss": {"true"}, "owner": {"alice"}, "requestId": {"req-1"}},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.AdminDeleteVolume(ctx, &koujav1.AdminDeleteVolumeRequest{VolumeId: "vol-1", AcknowledgeDataLoss: true, Owner: "alice", RequestId: "req-1"})
				return err
			},
		},
		{
			name: "GrantCredential", method: http.MethodPost, path: "/v1/admin/grants",
			wantBody: map[string]any{"nodeId": "node-1", "nodeCallerId": "caller-1"},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.GrantCredential(ctx, &koujav1.GrantCredentialRequest{VolumeId: "vol-1", NodeId: "node-1", NodeCallerId: "caller-1"})
				return err
			},
		},
		{
			name: "RevokeGrant", method: http.MethodDelete, path: "/v1/admin/grants/vol-1/node-1",
			query: url.Values{"owner": {"alice"}, "requestId": {"req-1"}},
			invoke: func(ctx context.Context, client *HTTP) error {
				_, err := client.RevokeGrant(ctx, &koujav1.RevokeGrantRequest{VolumeId: "vol-1", NodeId: "node-1", Owner: "alice", RequestId: "req-1"})
				return err
			},
		},
	}
	for _, call := range calls {
		runHTTPCall(t, call)
	}
}

func runHTTPCall(t testing.TB, call httpCall) {
	t.Helper()
	var got recordedHTTPRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		got = recordedHTTPRequest{
			method: request.Method,
			path:   request.URL.Path,
			query:  request.URL.Query(),
			header: request.Header.Clone(),
			body:   body,
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte("{}"))
	}))
	defer server.Close()

	client, err := NewHTTP(server.URL)
	if err != nil {
		t.Fatalf("%s: NewHTTP: %v", call.name, err)
	}
	if err := call.invoke(context.Background(), client); err != nil {
		t.Fatalf("%s: %v", call.name, err)
	}
	if got.method != call.method {
		t.Fatalf("%s: method = %s, want %s", call.name, got.method, call.method)
	}
	if got.path != call.path {
		t.Fatalf("%s: path = %s, want %s", call.name, got.path, call.path)
	}
	expectedQuery := call.query
	if expectedQuery == nil {
		expectedQuery = url.Values{}
	}
	if !reflect.DeepEqual(got.query, expectedQuery) {
		t.Fatalf("%s: query = %v, want %v", call.name, got.query, expectedQuery)
	}
	if call.wantBody == nil {
		if len(got.body) != 0 {
			t.Fatalf("%s: unexpected body %q", call.name, got.body)
		}
		return
	}
	if got.header.Get("Content-Type") != "application/json" {
		t.Fatalf("%s: Content-Type = %q", call.name, got.header.Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("%s: decode body: %v", call.name, err)
	}
	for key, want := range call.wantBody {
		if body[key] != want {
			t.Fatalf("%s: body[%q] = %#v, want %#v (body %s)", call.name, key, body[key], want, got.body)
		}
	}
}

func bddTestHTTPClientBasePathAndRequestID(t testing.TB) {
	var gotPath, gotRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotRequestID = request.Header.Get("X-Request-Id")
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"hostUuid":"host-1","ready":true}`))
	}))
	defer server.Close()

	client, err := NewHTTP(server.URL + "/api/")
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	info, err := client.GetInfo(WithRequestID(context.Background(), "req-1"), &koujav1.GetInfoRequest{})
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if gotPath != "/api/v1/info" {
		t.Fatalf("path = %q, want /api/v1/info", gotPath)
	}
	if gotRequestID != "req-1" {
		t.Fatalf("X-Request-Id = %q, want req-1", gotRequestID)
	}
	if info.GetHostUuid() != "host-1" || !info.GetReady() {
		t.Fatalf("GetInfo = %#v", info)
	}
}

func bddTestHTTPClientResponseDecoding(t testing.TB) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"desired": {
				"volumeId": "vol-1",
				"state": "VOLUME_STATE_AVAILABLE",
				"requestedSizeBytes": "1024"
			},
			"driftReasons": ["DRIFT_REASON_SIZE_MISMATCH"],
			"operationId": "op-1"
		}`))
	}))
	defer server.Close()

	client, err := NewHTTP(server.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	response, err := client.GetVolume(context.Background(), &koujav1.GetVolumeRequest{VolumeId: "vol-1"})
	if err != nil {
		t.Fatalf("GetVolume: %v", err)
	}
	if response.GetDesired().GetVolumeId() != "vol-1" {
		t.Fatalf("volume id = %q", response.GetDesired().GetVolumeId())
	}
	if response.GetDesired().GetState() != koujav1.VolumeState_VOLUME_STATE_AVAILABLE {
		t.Fatalf("state = %v", response.GetDesired().GetState())
	}
	if response.GetDesired().GetRequestedSizeBytes() != 1024 {
		t.Fatalf("size = %d", response.GetDesired().GetRequestedSizeBytes())
	}
	if len(response.GetDriftReasons()) != 1 || response.GetDriftReasons()[0] != koujav1.DriftReason_DRIFT_REASON_SIZE_MISMATCH {
		t.Fatalf("drift reasons = %v", response.GetDriftReasons())
	}
	if response.GetOperationId() != "op-1" {
		t.Fatalf("operation id = %q", response.GetOperationId())
	}
}

func bddTestHTTPClientErrors(t testing.TB) {
	statusServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusNotFound)
		_, _ = response.Write([]byte(`{"code":5,"message":"volume not found"}`))
	}))
	defer statusServer.Close()
	client, err := NewHTTP(statusServer.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	_, err = client.GetVolume(context.Background(), &koujav1.GetVolumeRequest{VolumeId: "missing"})
	var httpErr *Error
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if httpErr.HTTPStatus != http.StatusNotFound {
		t.Fatalf("HTTPStatus = %d", httpErr.HTTPStatus)
	}
	if httpErr.Code != codes.NotFound {
		t.Fatalf("Code = %v", httpErr.Code)
	}
	if httpErr.Message != "volume not found" {
		t.Fatalf("Message = %q", httpErr.Message)
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("status.Code = %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "volume not found") {
		t.Fatalf("Error() = %q", err.Error())
	}

	textServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain")
		response.WriteHeader(http.StatusBadGateway)
		_, _ = response.Write([]byte("bad gateway"))
	}))
	defer textServer.Close()
	textClient, err := NewHTTP(textServer.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	_, err = textClient.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	if !errors.As(err, &httpErr) {
		t.Fatalf("text error = %v, want *Error", err)
	}
	if httpErr.HTTPStatus != http.StatusBadGateway || httpErr.Code != codes.Unknown || httpErr.Message != "bad gateway" {
		t.Fatalf("text error = %#v", httpErr)
	}
	if status.Code(err) != codes.Unknown {
		t.Fatalf("status.Code = %v", status.Code(err))
	}

	emptyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer emptyServer.Close()
	emptyClient, err := NewHTTP(emptyServer.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	if _, err := emptyClient.GetInfo(context.Background(), &koujav1.GetInfoRequest{}); err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("empty response error = %v", err)
	}

	noContentServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer noContentServer.Close()
	noContentClient, err := NewHTTP(noContentServer.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	if _, err := noContentClient.GetInfo(context.Background(), &koujav1.GetInfoRequest{}); err != nil {
		t.Fatalf("204 response: %v", err)
	}

	closedServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closedServer.URL
	closedServer.Close()
	closedClient, err := NewHTTP(closedURL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	_, err = closedClient.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	if err == nil {
		t.Fatal("transport error was ignored")
	}
	if errors.As(err, &httpErr) {
		t.Fatalf("transport error was reported as *Error: %v", err)
	}
}

func bddTestHTTPClientValidation(t testing.TB) {
	for _, baseURL := range []string{"", "ftp://host", "https://", "http://host?query=1", "http://host#fragment"} {
		if _, err := NewHTTP(baseURL); err == nil {
			t.Fatalf("NewHTTP(%q) succeeded", baseURL)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	if _, err := NewHTTP(server.URL, WithHTTPClient(nil)); err == nil {
		t.Fatal("WithHTTPClient(nil) succeeded")
	}
	if _, err := NewHTTP(server.URL, WithTLSConfig(nil)); err == nil {
		t.Fatal("WithTLSConfig(nil) succeeded")
	}
	certFile, keyFile, caFile := writeTestCertificates(t)
	if _, err := NewHTTPFromFiles(server.URL, certFile, keyFile, caFile); err != nil {
		t.Fatalf("NewHTTPFromFiles: %v", err)
	}
	if _, err := NewHTTPFromFiles(server.URL, "missing.crt", "missing.key", "missing-ca.crt"); err == nil {
		t.Fatal("NewHTTPFromFiles accepted missing files")
	}
	client, err := NewHTTP(server.URL, nil)
	if err != nil {
		t.Fatalf("NewHTTP with nil option: %v", err)
	}
	if _, err := client.DeleteVolume(context.Background(), &koujav1.DeleteVolumeRequest{}); err == nil {
		t.Fatal("DeleteVolume accepted an empty volume ID")
	}
	if _, err := client.RevokeGrant(context.Background(), &koujav1.RevokeGrantRequest{VolumeId: "vol-1"}); err == nil {
		t.Fatal("RevokeGrant accepted an empty node ID")
	}
	client.CloseIdleConnections()
	var nilClient *HTTP
	nilClient.CloseIdleConnections()
}
