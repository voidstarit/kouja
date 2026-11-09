package terraformprovider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/voidstarit/kouja/gen/client"
	"github.com/voidstarit/kouja/gen/kouja/v1"
)

type koujaProvider struct {
	version string
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &koujaProvider{version: version} }
}

func (p *koujaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, response *provider.MetadataResponse) {
	response.TypeName = "kouja"
	response.Version = p.version
}

func (p *koujaProvider) Schema(_ context.Context, _ provider.SchemaRequest, response *provider.SchemaResponse) {
	response.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"endpoint":         schema.StringAttribute{Required: true, Description: "kouja agent HTTPS endpoint."},
		"server_ca_pem":    schema.StringAttribute{Optional: true, Description: "Inline PEM for the kouja server CA."},
		"server_ca_file":   schema.StringAttribute{Optional: true, Description: "File containing the kouja server CA."},
		"client_cert_pem":  schema.StringAttribute{Optional: true, Description: "Inline admin client certificate."},
		"client_cert_file": schema.StringAttribute{Optional: true, Description: "File containing the admin client certificate."},
		"client_key_pem":   schema.StringAttribute{Optional: true, Sensitive: true, Description: "Inline admin client private key."},
		"client_key_file":  schema.StringAttribute{Optional: true, Sensitive: true, Description: "File containing the admin client private key."},
		"server_name":      schema.StringAttribute{Optional: true, Description: "TLS server name override."},
		"timeout":          schema.StringAttribute{Optional: true, Description: "Request timeout, for example 30s."},
	}}
}

type providerModel struct {
	Endpoint       types.String `tfsdk:"endpoint"`
	ServerCAPEM    types.String `tfsdk:"server_ca_pem"`
	ServerCAFile   types.String `tfsdk:"server_ca_file"`
	ClientCertPEM  types.String `tfsdk:"client_cert_pem"`
	ClientCertFile types.String `tfsdk:"client_cert_file"`
	ClientKeyPEM   types.String `tfsdk:"client_key_pem"`
	ClientKeyFile  types.String `tfsdk:"client_key_file"`
	ServerName     types.String `tfsdk:"server_name"`
	Timeout        types.String `tfsdk:"timeout"`
}

type providerData struct {
	client      providerClient
	ownSerial   string
	ownNotAfter time.Time
}

func (p *koujaProvider) Configure(ctx context.Context, request provider.ConfigureRequest, response *provider.ConfigureResponse) {
	var config providerModel
	response.Diagnostics.Append(request.Config.Get(ctx, &config)...)
	if response.Diagnostics.HasError() {
		return
	}
	endpoint := strings.TrimSpace(config.Endpoint.ValueString())
	if endpoint == "" {
		endpoint = os.Getenv("KOUJA_ENDPOINT")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		response.Diagnostics.AddError("Invalid endpoint", "endpoint must be an absolute https://host:port URL with no path, query, or fragment")
		return
	}
	timeout := 30 * time.Second
	if value := firstString(config.Timeout, os.Getenv("KOUJA_TIMEOUT")); value != "" {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			response.Diagnostics.AddError("Invalid timeout", "timeout must be a positive Go duration")
			return
		}
	}
	caPEM, err := resolveMaterial(config.ServerCAPEM, config.ServerCAFile, "KOUJA_SERVER_CA_PEM", "KOUJA_SERVER_CA_FILE", false)
	if err != nil {
		response.Diagnostics.AddError("Invalid server CA configuration", err.Error())
		return
	}
	certPEM, err := resolveMaterial(config.ClientCertPEM, config.ClientCertFile, "KOUJA_CLIENT_CERT_PEM", "KOUJA_CLIENT_CERT_FILE", true)
	if err != nil {
		response.Diagnostics.AddError("Invalid client certificate configuration", err.Error())
		return
	}
	keyPEM, err := resolveMaterial(config.ClientKeyPEM, config.ClientKeyFile, "KOUJA_CLIENT_KEY_PEM", "KOUJA_CLIENT_KEY_FILE", true)
	if err != nil {
		response.Diagnostics.AddError("Invalid client key configuration", err.Error())
		return
	}
	tlsConfig, ownSerial, ownNotAfter, err := buildTLSConfig(caPEM, certPEM, keyPEM, firstString(config.ServerName, os.Getenv("KOUJA_SERVER_NAME")), parsed.Hostname())
	if err != nil {
		response.Diagnostics.AddError("Invalid TLS configuration", err.Error())
		return
	}
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
			Proxy:           http.ProxyFromEnvironment,
		},
	}
	api, err := client.NewHTTP(endpoint, client.WithHTTPClient(httpClient))
	if err != nil {
		response.Diagnostics.AddError("Invalid client", err.Error())
		return
	}
	wrapped := &httpProviderClient{http: api}
	info, err := wrapped.GetInfo(ctx)
	if err != nil {
		response.Diagnostics.AddError("Unable to query kouja agent", err.Error())
		return
	}
	if !contains(info.GetCapabilities(), "certificates") || !contains(info.GetCapabilities(), "storage_classes") {
		response.Diagnostics.AddError("Agent does not support the Terraform provider", "the agent must advertise certificates and storage_classes capabilities")
		return
	}
	if info.GetCallerRole() != "admin" {
		response.Diagnostics.AddError("Provider certificate is not an admin", "provider client certificate must have the admin role")
		return
	}
	response.DataSourceData = &providerData{client: wrapped, ownSerial: ownSerial, ownNotAfter: ownNotAfter}
	response.ResourceData = response.DataSourceData
}

func (p *koujaProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }

func (p *koujaProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource { return &volumeGroupResource{} },
		func() resource.Resource { return &storageClassResource{} },
		func() resource.Resource { return &clientCertificateResource{} },
	}
}

func resolveMaterial(pemValue, fileValue types.String, pemEnv, fileEnv string, required bool) ([]byte, error) {
	pemText := concreteString(pemValue)
	filePath := concreteString(fileValue)
	if pemText == "" && filePath == "" {
		pemText = strings.TrimSpace(os.Getenv(pemEnv))
		filePath = strings.TrimSpace(os.Getenv(fileEnv))
	}
	if pemText != "" && filePath != "" {
		return nil, fmt.Errorf("exactly one of the PEM and file forms may be set")
	}
	if pemText != "" {
		return []byte(pemText), nil
	}
	if filePath != "" {
		return os.ReadFile(filePath) //nolint:gosec // operator-selected provider file
	}
	if required {
		return nil, fmt.Errorf("a PEM or file value is required")
	}
	return nil, nil
}

func concreteString(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}
	return strings.TrimSpace(value.ValueString())
}

func buildTLSConfig(caPEM, certPEM, keyPEM []byte, serverName, endpointHost string) (*tls.Config, string, time.Time, error) {
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("client certificate and key: %w", err)
	}
	leafBlock, _ := pem.Decode(certPEM)
	if leafBlock == nil {
		return nil, "", time.Time{}, fmt.Errorf("client certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("parse client certificate: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if len(caPEM) > 0 && !roots.AppendCertsFromPEM(caPEM) {
		return nil, "", time.Time{}, fmt.Errorf("server CA contains no certificates")
	}
	if serverName == "" {
		serverName = endpointHost
	}
	return &tls.Config{ //nolint:gosec // kouja requires TLS 1.3
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      roots,
		ServerName:   serverName,
	}, leaf.SerialNumber.Text(16), leaf.NotAfter, nil
}

func firstString(value types.String, fallback string) string {
	if !value.IsNull() && !value.IsUnknown() && strings.TrimSpace(value.ValueString()) != "" {
		return strings.TrimSpace(value.ValueString())
	}
	return strings.TrimSpace(fallback)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func addWarnings(response any, values []*koujav1.Warning) {
	for _, value := range values {
		switch typed := response.(type) {
		case *resource.CreateResponse:
			typed.Diagnostics.AddWarning(value.GetCode(), value.GetMessage())
		case *resource.ReadResponse:
			typed.Diagnostics.AddWarning(value.GetCode(), value.GetMessage())
		case *resource.UpdateResponse:
			typed.Diagnostics.AddWarning(value.GetCode(), value.GetMessage())
		case *resource.DeleteResponse:
			typed.Diagnostics.AddWarning(value.GetCode(), value.GetMessage())
		}
	}
}

func diagnosticFromError(err error) diag.Diagnostic {
	var clientErr *client.Error
	if errors.As(err, &clientErr) {
		return diag.NewErrorDiagnostic(clientErr.Reason, clientErr.Message)
	}
	return diag.NewErrorDiagnostic("kouja API error", err.Error())
}

func isNotFound(err error) bool {
	var clientErr *client.Error
	return errors.As(err, &clientErr) && clientErr.Code == 5
}
