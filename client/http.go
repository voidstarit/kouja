package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const maxErrorBodyBytes = 1 << 20

// HTTP is a typed HTTP/JSON client for the kouja REST API. It implements the
// same operations as the gRPC services.
type HTTP struct {
	baseURL string
	client  *http.Client
}

type httpOptions struct {
	client *http.Client
}

// HTTPOption configures an HTTP client.
type HTTPOption func(*httpOptions) error

// WithHTTPClient configures the underlying HTTP client. Use this to provide a
// custom transport, timeout, or instrumentation.
func WithHTTPClient(httpClient *http.Client) HTTPOption {
	return func(options *httpOptions) error {
		if httpClient == nil {
			return fmt.Errorf("client: HTTP client is nil")
		}
		options.client = httpClient
		return nil
	}
}

// WithTLSConfig configures an mTLS HTTP transport. The TLS configuration must
// contain a client certificate trusted by the agent and the CA used to verify
// the agent certificate.
func WithTLSConfig(tlsConfig *tls.Config) HTTPOption {
	return func(options *httpOptions) error {
		if tlsConfig == nil {
			return fmt.Errorf("client: TLS configuration is nil")
		}
		transport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return fmt.Errorf("client: default HTTP transport is not *http.Transport")
		}
		cloned := transport.Clone()
		cloned.TLSClientConfig = tlsConfig.Clone()
		options.client = &http.Client{Transport: cloned}
		return nil
	}
}

// NewHTTP creates an HTTP/JSON client for baseURL, which may include a path
// prefix. Use [WithTLSConfig] for mTLS, or pass a prepared client with
// [WithHTTPClient].
func NewHTTP(baseURL string, opts ...HTTPOption) (*HTTP, error) {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	options := &httpOptions{client: http.DefaultClient}
	for _, option := range opts {
		if option == nil {
			continue
		}
		if err := option(options); err != nil {
			return nil, err
		}
	}
	if options.client == nil {
		return nil, fmt.Errorf("client: HTTP client is nil")
	}
	return &HTTP{baseURL: normalized, client: options.client}, nil
}

// NewHTTPFromFiles creates an HTTP/JSON client using certificate files on
// disk.
func NewHTTPFromFiles(baseURL, certFile, keyFile, caFile string, opts ...HTTPOption) (*HTTP, error) {
	tlsConfig, err := LoadTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	options := make([]HTTPOption, 0, len(opts)+1)
	options = append(options, WithTLSConfig(tlsConfig))
	options = append(options, opts...)
	return NewHTTP(baseURL, options...)
}

// CloseIdleConnections closes idle connections held by the underlying HTTP
// client.
func (client *HTTP) CloseIdleConnections() {
	if client != nil && client.client != nil {
		client.client.CloseIdleConnections()
	}
}

// CreateVolume creates a volume in a registered volume group.
func (client *HTTP) CreateVolume(ctx context.Context, request *koujav1.CreateVolumeRequest) (*koujav1.CreateVolumeResponse, error) {
	if request == nil {
		request = &koujav1.CreateVolumeRequest{}
	}
	response := &koujav1.CreateVolumeResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/volumes", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ExportVolume exports a volume to one initiator.
func (client *HTTP) ExportVolume(ctx context.Context, request *koujav1.ExportVolumeRequest) (*koujav1.ExportVolumeResponse, error) {
	if request == nil {
		request = &koujav1.ExportVolumeRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	response := &koujav1.ExportVolumeResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/volumes/"+volumeID+":export", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// UnexportVolume removes a volume export.
func (client *HTTP) UnexportVolume(ctx context.Context, request *koujav1.UnexportVolumeRequest) (*koujav1.UnexportVolumeResponse, error) {
	if request == nil {
		request = &koujav1.UnexportVolumeRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	response := &koujav1.UnexportVolumeResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/volumes/"+volumeID+":unexport", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ResizeVolume grows or shrinks a volume.
func (client *HTTP) ResizeVolume(ctx context.Context, request *koujav1.ResizeVolumeRequest) (*koujav1.ResizeVolumeResponse, error) {
	if request == nil {
		request = &koujav1.ResizeVolumeRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	response := &koujav1.ResizeVolumeResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/volumes/"+volumeID+":resize", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// DeleteVolume deletes a volume.
func (client *HTTP) DeleteVolume(ctx context.Context, request *koujav1.DeleteVolumeRequest) (*koujav1.DeleteVolumeResponse, error) {
	if request == nil {
		request = &koujav1.DeleteVolumeRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	addBool(query, "force", request.GetForce())
	addString(query, "requestId", request.GetRequestId())
	response := &koujav1.DeleteVolumeResponse{}
	if err := client.do(ctx, http.MethodDelete, "/v1/volumes/"+volumeID, query, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// GetVolume returns the desired and observed state of a volume.
func (client *HTTP) GetVolume(ctx context.Context, request *koujav1.GetVolumeRequest) (*koujav1.GetVolumeResponse, error) {
	if request == nil {
		request = &koujav1.GetVolumeRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	response := &koujav1.GetVolumeResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/volumes/"+volumeID, nil, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ListVolumes lists volumes visible to the caller.
func (client *HTTP) ListVolumes(ctx context.Context, request *koujav1.ListVolumesRequest) (*koujav1.ListVolumesResponse, error) {
	if request == nil {
		request = &koujav1.ListVolumesRequest{}
	}
	query := url.Values{}
	addInt32(query, "pageSize", request.GetPageSize())
	addString(query, "pageToken", request.GetPageToken())
	response := &koujav1.ListVolumesResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/volumes", query, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// GetCredential returns the export credential for a volume.
func (client *HTTP) GetCredential(ctx context.Context, request *koujav1.GetCredentialRequest) (*koujav1.GetCredentialResponse, error) {
	if request == nil {
		request = &koujav1.GetCredentialRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	addString(query, "owner", request.GetOwner())
	response := &koujav1.GetCredentialResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/volumes/"+volumeID+"/credential", query, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// RotateCredential rotates the export credential for a volume.
func (client *HTTP) RotateCredential(ctx context.Context, request *koujav1.RotateCredentialRequest) (*koujav1.RotateCredentialResponse, error) {
	if request == nil {
		request = &koujav1.RotateCredentialRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	response := &koujav1.RotateCredentialResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/volumes/"+volumeID+":rotate-credential", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// GetCapacity returns capacity for every registered volume group.
func (client *HTTP) GetCapacity(ctx context.Context, _ *koujav1.GetCapacityRequest) (*koujav1.GetCapacityResponse, error) {
	response := &koujav1.GetCapacityResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/capacity", nil, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ListStorageClasses returns the storage classes configured on the agent.
func (client *HTTP) ListStorageClasses(ctx context.Context, _ *koujav1.ListStorageClassesRequest) (*koujav1.ListStorageClassesResponse, error) {
	response := &koujav1.ListStorageClassesResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/storage-classes", nil, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// GetInfo returns host identity and API capability information.
func (client *HTTP) GetInfo(ctx context.Context, _ *koujav1.GetInfoRequest) (*koujav1.GetInfoResponse, error) {
	response := &koujav1.GetInfoResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/info", nil, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// RegisterVG registers a volume group with the agent.
func (client *HTTP) RegisterVG(ctx context.Context, request *koujav1.RegisterVGRequest) (*koujav1.RegisterVGResponse, error) {
	if request == nil {
		request = &koujav1.RegisterVGRequest{}
	}
	response := &koujav1.RegisterVGResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/admin/vgs", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// DeregisterVG deregisters a volume group.
func (client *HTTP) DeregisterVG(ctx context.Context, request *koujav1.DeregisterVGRequest) (*koujav1.DeregisterVGResponse, error) {
	if request == nil {
		request = &koujav1.DeregisterVGRequest{}
	}
	vgName, err := pathParam("vg_name", request.GetVgName())
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	addString(query, "requestId", request.GetRequestId())
	response := &koujav1.DeregisterVGResponse{}
	if err := client.do(ctx, http.MethodDelete, "/v1/admin/vgs/"+vgName, query, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ListVGs lists every registered volume group.
func (client *HTTP) ListVGs(ctx context.Context, _ *koujav1.ListVGsRequest) (*koujav1.ListVGsResponse, error) {
	response := &koujav1.ListVGsResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/admin/vgs", nil, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ImportVolume imports an existing logical volume and optional export.
func (client *HTTP) ImportVolume(ctx context.Context, request *koujav1.ImportVolumeRequest) (*koujav1.ImportVolumeResponse, error) {
	if request == nil {
		request = &koujav1.ImportVolumeRequest{}
	}
	response := &koujav1.ImportVolumeResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/admin/import", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ResolveDrift applies an administrator-selected drift action.
func (client *HTTP) ResolveDrift(ctx context.Context, request *koujav1.ResolveDriftRequest) (*koujav1.ResolveDriftResponse, error) {
	if request == nil {
		request = &koujav1.ResolveDriftRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	response := &koujav1.ResolveDriftResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/admin/drift/"+volumeID+":resolve", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// ListOrphans lists volumes that have no matching export or host state.
func (client *HTTP) ListOrphans(ctx context.Context, _ *koujav1.ListOrphansRequest) (*koujav1.ListOrphansResponse, error) {
	response := &koujav1.ListOrphansResponse{}
	if err := client.do(ctx, http.MethodGet, "/v1/admin/orphans", nil, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// WipeVolume destroys the data on a volume.
func (client *HTTP) WipeVolume(ctx context.Context, request *koujav1.WipeVolumeRequest) (*koujav1.WipeVolumeResponse, error) {
	if request == nil {
		request = &koujav1.WipeVolumeRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	response := &koujav1.WipeVolumeResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/admin/volumes/"+volumeID+":wipe", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// TransferOwnership transfers a volume to a new owner.
func (client *HTTP) TransferOwnership(ctx context.Context, request *koujav1.TransferOwnershipRequest) (*koujav1.TransferOwnershipResponse, error) {
	if request == nil {
		request = &koujav1.TransferOwnershipRequest{}
	}
	response := &koujav1.TransferOwnershipResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/admin/ownership:transfer", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// AdminDeleteVolume deletes a volume as an administrator.
func (client *HTTP) AdminDeleteVolume(ctx context.Context, request *koujav1.AdminDeleteVolumeRequest) (*koujav1.AdminDeleteVolumeResponse, error) {
	if request == nil {
		request = &koujav1.AdminDeleteVolumeRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	addBool(query, "acknowledgeDataLoss", request.GetAcknowledgeDataLoss())
	addString(query, "owner", request.GetOwner())
	addString(query, "requestId", request.GetRequestId())
	response := &koujav1.AdminDeleteVolumeResponse{}
	if err := client.do(ctx, http.MethodDelete, "/v1/admin/volumes/"+volumeID, query, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// GrantCredential grants a node access to a volume credential.
func (client *HTTP) GrantCredential(ctx context.Context, request *koujav1.GrantCredentialRequest) (*koujav1.GrantCredentialResponse, error) {
	if request == nil {
		request = &koujav1.GrantCredentialRequest{}
	}
	response := &koujav1.GrantCredentialResponse{}
	if err := client.do(ctx, http.MethodPost, "/v1/admin/grants", nil, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// RevokeGrant revokes a node credential grant.
func (client *HTTP) RevokeGrant(ctx context.Context, request *koujav1.RevokeGrantRequest) (*koujav1.RevokeGrantResponse, error) {
	if request == nil {
		request = &koujav1.RevokeGrantRequest{}
	}
	volumeID, err := pathParam("volume_id", request.GetVolumeId())
	if err != nil {
		return nil, err
	}
	nodeID, err := pathParam("node_id", request.GetNodeId())
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	addString(query, "owner", request.GetOwner())
	addString(query, "requestId", request.GetRequestId())
	response := &koujav1.RevokeGrantResponse{}
	if err := client.do(ctx, http.MethodDelete, "/v1/admin/grants/"+volumeID+"/"+nodeID, query, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

// Error is returned when the agent responds with a non-2xx HTTP status. The
// code and message come from the grpc-gateway google.rpc.Status error body when
// one is present.
type Error struct {
	HTTPStatus int
	Code       codes.Code
	Message    string
	Details    []*anypb.Any
}

// Error implements error.
func (err *Error) Error() string {
	if err == nil {
		return "client: <nil>"
	}
	if err.HTTPStatus != 0 {
		statusText := http.StatusText(err.HTTPStatus)
		if statusText == "" {
			statusText = "HTTP error"
		}
		if err.Message == "" {
			return fmt.Sprintf("client: HTTP %d %s", err.HTTPStatus, statusText)
		}
		return fmt.Sprintf("client: HTTP %d %s: %s", err.HTTPStatus, statusText, err.Message)
	}
	if err.Message == "" {
		return fmt.Sprintf("client: %s", err.Code)
	}
	return fmt.Sprintf("client: %s: %s", err.Code, err.Message)
}

// GRPCStatus lets grpc/status translate an HTTP error into a gRPC status.
func (err *Error) GRPCStatus() *status.Status {
	if err == nil {
		return status.New(codes.Unknown, "")
	}
	code := err.Code
	if code == codes.OK {
		code = codes.Unknown
	}
	return status.New(code, err.Message)
}

func (client *HTTP) do(ctx context.Context, method, path string, query url.Values, request, response proto.Message) error {
	var body io.Reader
	if request != nil {
		payload, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal(request)
		if err != nil {
			return fmt.Errorf("client: marshal %s request: %w", path, err)
		}
		body = bytes.NewReader(payload)
	}
	requestURL := client.baseURL + path
	if len(query) != 0 {
		requestURL += "?" + query.Encode()
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return fmt.Errorf("client: create %s request: %w", path, err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	if request != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if requestID := RequestIDFromContext(ctx); requestID != "" {
		httpRequest.Header.Set("X-Request-Id", requestID)
	}
	httpResponse, err := client.client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("client: %s %s: %w", method, requestURL, err)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return decodeHTTPError(httpResponse)
	}
	if response == nil || httpResponse.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, httpResponse.Body)
		return nil
	}
	payload, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		return fmt.Errorf("client: read %s response: %w", path, err)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return fmt.Errorf("client: %s returned an empty response", path)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(payload, response); err != nil {
		return fmt.Errorf("client: decode %s response: %w", path, err)
	}
	return nil
}

func decodeHTTPError(response *http.Response) error {
	result := &Error{HTTPStatus: response.StatusCode, Code: codes.Unknown}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if err != nil {
		result.Message = response.Status
		return result
	}
	var protoStatus statuspb.Status
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(payload, &protoStatus); err == nil &&
		(protoStatus.GetCode() != 0 || protoStatus.GetMessage() != "") {
		result.Code = codes.Code(protoStatus.GetCode()) //nolint:gosec // google.rpc.Code values are non-negative
		result.Message = protoStatus.GetMessage()
		result.Details = protoStatus.GetDetails()
	} else {
		result.Message = strings.TrimSpace(string(payload))
	}
	if result.Message == "" {
		result.Message = response.Status
	}
	return result
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("client: base URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("client: parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("client: base URL must use http or https: %q", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("client: base URL must include a host: %q", raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("client: base URL must not include a query or fragment: %q", raw)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func pathParam(name, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("client: %s is required", name)
	}
	return url.PathEscape(value), nil
}

func addString(values url.Values, name, value string) {
	if value != "" {
		values.Set(name, value)
	}
}

func addBool(values url.Values, name string, value bool) {
	if value {
		values.Set(name, "true")
	}
}

func addInt32(values url.Values, name string, value int32) {
	if value != 0 {
		values.Set(name, strconv.FormatInt(int64(value), 10))
	}
}
