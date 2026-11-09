package terraformprovider

import (
	"context"

	"github.com/voidstarit/kouja/gen/client"
	"github.com/voidstarit/kouja/gen/kouja/v1"
)

type providerClient interface {
	GetInfo(ctx context.Context) (*koujav1.GetInfoResponse, error)
	GetVG(ctx context.Context, name string) (*koujav1.GetVGResponse, error)
	RegisterVG(ctx context.Context, name, expectedUUID, requestID string) (*koujav1.RegisterVGResponse, error)
	DeregisterVG(ctx context.Context, name, requestID string) error
	ListClasses(ctx context.Context) (*koujav1.ListClassesResponse, error)
	GetClass(ctx context.Context, name string) (*koujav1.ClassMapping, error)
	SetClass(ctx context.Context, name, vg, requestID string) (*koujav1.SetClassResponse, error)
	RemoveClass(ctx context.Context, name, requestID string) (*koujav1.RemoveClassResponse, error)
	IssueCertificate(ctx context.Context, role, name string, ttlDays int32, requestID string) (*koujav1.IssueCertificateResponse, error)
	GetCertificate(ctx context.Context, serial string) (*koujav1.GetCertificateResponse, error)
	RevokeCertificate(ctx context.Context, serial, reason, requestID string) (*koujav1.RevokeCertificateResponse, error)
}

type httpProviderClient struct {
	http *client.HTTP
}

func (c *httpProviderClient) GetInfo(ctx context.Context) (*koujav1.GetInfoResponse, error) {
	return c.http.GetInfo(ctx, &koujav1.GetInfoRequest{})
}

func (c *httpProviderClient) GetVG(ctx context.Context, name string) (*koujav1.GetVGResponse, error) {
	return c.http.GetVG(ctx, &koujav1.GetVGRequest{VgName: name})
}

func (c *httpProviderClient) RegisterVG(ctx context.Context, name, expectedUUID, requestID string) (*koujav1.RegisterVGResponse, error) {
	ctx = client.WithRequestID(ctx, requestID)
	return c.http.RegisterVG(ctx, &koujav1.RegisterVGRequest{VgName: name, VgUuid: expectedUUID, RequestId: requestID})
}

func (c *httpProviderClient) DeregisterVG(ctx context.Context, name, requestID string) error {
	ctx = client.WithRequestID(ctx, requestID)
	_, err := c.http.DeregisterVG(ctx, &koujav1.DeregisterVGRequest{VgName: name, RequestId: requestID})
	return err
}

func (c *httpProviderClient) ListClasses(ctx context.Context) (*koujav1.ListClassesResponse, error) {
	return c.http.ListClasses(ctx, &koujav1.ListClassesRequest{})
}

func (c *httpProviderClient) GetClass(ctx context.Context, name string) (*koujav1.ClassMapping, error) {
	response, err := c.http.GetClass(ctx, &koujav1.GetClassRequest{ClassName: name})
	if err != nil {
		return nil, err
	}
	return response.GetClass(), nil
}

func (c *httpProviderClient) SetClass(ctx context.Context, name, vg, requestID string) (*koujav1.SetClassResponse, error) {
	ctx = client.WithRequestID(ctx, requestID)
	return c.http.SetClass(ctx, &koujav1.SetClassRequest{ClassName: name, VgName: vg, RequestId: requestID})
}

func (c *httpProviderClient) RemoveClass(ctx context.Context, name, requestID string) (*koujav1.RemoveClassResponse, error) {
	ctx = client.WithRequestID(ctx, requestID)
	return c.http.RemoveClass(ctx, &koujav1.RemoveClassRequest{ClassName: name, RequestId: requestID})
}

func (c *httpProviderClient) IssueCertificate(ctx context.Context, role, name string, ttlDays int32, requestID string) (*koujav1.IssueCertificateResponse, error) {
	ctx = client.WithRequestID(ctx, requestID)
	return c.http.IssueCertificate(ctx, &koujav1.IssueCertificateRequest{Role: role, Name: name, TtlDays: ttlDays, RequestId: requestID})
}

func (c *httpProviderClient) GetCertificate(ctx context.Context, serial string) (*koujav1.GetCertificateResponse, error) {
	return c.http.GetCertificate(ctx, &koujav1.GetCertificateRequest{SerialNumber: serial})
}

func (c *httpProviderClient) RevokeCertificate(ctx context.Context, serial, reason, requestID string) (*koujav1.RevokeCertificateResponse, error) {
	ctx = client.WithRequestID(ctx, requestID)
	return c.http.RevokeCertificate(ctx, &koujav1.RevokeCertificateRequest{SerialNumber: serial, Reason: reason, RequestId: requestID})
}
