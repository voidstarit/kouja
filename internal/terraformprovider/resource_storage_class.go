package terraformprovider

import (
	"context"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type storageClassResource struct{ data *providerData }

type storageClassModel struct {
	Name types.String `tfsdk:"name"`
	VG   types.String `tfsdk:"vg"`
}

func (r *storageClassResource) Metadata(_ context.Context, _ resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = "kouja_storage_class"
}

func (r *storageClassResource) Configure(_ context.Context, request resource.ConfigureRequest, response *resource.ConfigureResponse) {
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

func (r *storageClassResource) Schema(_ context.Context, _ resource.SchemaRequest, response *resource.SchemaResponse) {
	response.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"name": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"vg":   schema.StringAttribute{Required: true},
	}}
}

func (r *storageClassResource) Create(ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse) {
	var plan storageClassModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	result, err := data.client.SetClass(ctx, plan.Name.ValueString(), plan.VG.ValueString(), uuid.NewString())
	if err != nil {
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	addWarnings(response, result.GetWarnings())
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *storageClassResource) Read(ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse) {
	var state storageClassModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	mapping, err := data.client.GetClass(ctx, state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			response.State.RemoveResource(ctx)
			return
		}
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	state.VG = types.StringValue(mapping.GetVgName())
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *storageClassResource) Update(ctx context.Context, request resource.UpdateRequest, response *resource.UpdateResponse) {
	var plan storageClassModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	result, err := data.client.SetClass(ctx, plan.Name.ValueString(), plan.VG.ValueString(), uuid.NewString())
	if err != nil {
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	addWarnings(response, result.GetWarnings())
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *storageClassResource) Delete(ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse) {
	var state storageClassModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	result, err := data.client.RemoveClass(ctx, state.Name.ValueString(), uuid.NewString())
	if err != nil && !isNotFound(err) {
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	if result != nil {
		addWarnings(response, result.GetWarnings())
	}
}

func (r *storageClassResource) ImportState(ctx context.Context, request resource.ImportStateRequest, response *resource.ImportStateResponse) {
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("name"), request.ID)...)
}
