package terraformprovider

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type clientCertificateResource struct{ data *providerData }

type clientCertificateModel struct {
	Role           types.String `tfsdk:"role"`
	Name           types.String `tfsdk:"name"`
	TTLDays        types.Int64  `tfsdk:"ttl_days"`
	SerialNumber   types.String `tfsdk:"serial_number"`
	CertificatePEM types.String `tfsdk:"certificate_pem"`
	PrivateKeyPEM  types.String `tfsdk:"private_key_pem"`
	ClientCAPEM    types.String `tfsdk:"client_ca_pem"`
	IssuedBy       types.String `tfsdk:"issued_by"`
	NotBefore      types.String `tfsdk:"not_before"`
	NotAfter       types.String `tfsdk:"not_after"`
	Revoked        types.Bool   `tfsdk:"revoked"`
}

func (r *clientCertificateResource) Metadata(_ context.Context, _ resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = "kouja_client_certificate"
}

func (r *clientCertificateResource) Configure(_ context.Context, request resource.ConfigureRequest, response *resource.ConfigureResponse) {
	if request.ProviderData == nil {
		return
	}
	data, ok := request.ProviderData.(*providerData)
	if !ok {
		response.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	r.data = data
}

func (r *clientCertificateResource) Schema(_ context.Context, _ resource.SchemaRequest, response *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	response.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"role":            schema.StringAttribute{Required: true, PlanModifiers: replace},
		"name":            schema.StringAttribute{Required: true, PlanModifiers: replace},
		"ttl_days":        schema.Int64Attribute{Optional: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
		"serial_number":   schema.StringAttribute{Computed: true},
		"certificate_pem": schema.StringAttribute{Computed: true},
		"private_key_pem": schema.StringAttribute{Computed: true, Sensitive: true},
		"client_ca_pem":   schema.StringAttribute{Computed: true},
		"issued_by":       schema.StringAttribute{Computed: true},
		"not_before":      schema.StringAttribute{Computed: true},
		"not_after":       schema.StringAttribute{Computed: true},
		"revoked":         schema.BoolAttribute{Computed: true},
	}}
}

func (r *clientCertificateResource) Create(ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse) {
	var plan clientCertificateModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	ttl := int32(0)
	if !plan.TTLDays.IsNull() && !plan.TTLDays.IsUnknown() {
		ttlValue := plan.TTLDays.ValueInt64()
		if ttlValue < 0 || ttlValue > 365 {
			response.Diagnostics.AddError("Invalid ttl_days", "ttl_days must be between 0 and 365")
			return
		}
		ttl = int32(ttlValue) //nolint:gosec // validated range
	}
	issued, err := data.client.IssueCertificate(ctx, plan.Role.ValueString(), plan.Name.ValueString(), ttl, uuid.NewString())
	if err != nil {
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	if issued.GetCertificatePem() == "" || issued.GetPrivateKeyPem() == "" || issued.GetClientCaPem() == "" || issued.GetSerialNumber() == "" {
		response.Diagnostics.AddError("Incomplete certificate response", "agent returned an incomplete certificate, key, CA, or serial")
		return
	}
	plan.SerialNumber = types.StringValue(issued.GetSerialNumber())
	plan.CertificatePEM = types.StringValue(issued.GetCertificatePem())
	plan.PrivateKeyPEM = types.StringValue(issued.GetPrivateKeyPem())
	plan.ClientCAPEM = types.StringValue(issued.GetClientCaPem())
	plan.IssuedBy = types.StringValue(issued.GetIssuerSerial())
	plan.NotBefore = types.StringValue(timestampString(issued.GetNotBefore()))
	plan.NotAfter = types.StringValue(timestampString(issued.GetNotAfter()))
	plan.Revoked = types.BoolValue(false)
	addWarnings(response, issued.GetWarnings())
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *clientCertificateResource) Read(ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse) {
	var state clientCertificateModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	responseCertificate, err := data.client.GetCertificate(ctx, state.SerialNumber.ValueString())
	if err != nil {
		if isNotFound(err) {
			response.State.RemoveResource(ctx)
			return
		}
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	certificate := responseCertificate.GetCertificate()
	if certificate.GetRevoked() || state.PrivateKeyPEM.IsNull() {
		response.State.RemoveResource(ctx)
		return
	}
	state.CertificatePEM = types.StringValue(certificate.GetCertificatePem())
	state.ClientCAPEM = types.StringValue(certificate.GetClientCaPem())
	state.IssuedBy = types.StringValue(certificate.GetIssuerSerial())
	state.NotBefore = types.StringValue(timestampString(certificate.GetNotBefore()))
	state.NotAfter = types.StringValue(timestampString(certificate.GetNotAfter()))
	state.Revoked = types.BoolValue(certificate.GetRevoked())
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *clientCertificateResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *clientCertificateResource) Delete(ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse) {
	var state clientCertificateModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	serial := state.SerialNumber.ValueString()
	if serial != "" && serial == data.ownSerial {
		response.Diagnostics.AddError("Refusing to revoke the provider certificate", "the provider's own admin certificate cannot be revoked by the provider")
		return
	}
	result, err := data.client.RevokeCertificate(ctx, serial, "terraform_destroy", uuid.NewString())
	if err != nil {
		if isNotFound(err) {
			return
		}
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	addWarnings(response, result.GetWarnings())
}

func timestampString(timestamp *timestamppb.Timestamp) string {
	if timestamp == nil {
		return ""
	}
	return timestamp.AsTime().UTC().Format(time.RFC3339)
}
