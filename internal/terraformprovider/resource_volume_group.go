package terraformprovider

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/voidstarit/kouja/gen/kouja/v1"
)

type volumeGroupResource struct{ data *providerData }

type volumeGroupModel struct {
	Name             types.String `tfsdk:"name"`
	RegisteredVGUUID types.String `tfsdk:"registered_vg_uuid"`
	ObservedVGUUID   types.String `tfsdk:"observed_vg_uuid"`
	UUIDDrift        types.Bool   `tfsdk:"uuid_drift"`
	State            types.String `tfsdk:"state"`
	StorageClass     types.String `tfsdk:"storage_class"`
	TotalBytes       types.Int64  `tfsdk:"total_bytes"`
	FreeBytes        types.Int64  `tfsdk:"free_bytes"`
	AllocatableBytes types.Int64  `tfsdk:"allocatable_bytes"`
	ExtentSizeBytes  types.Int64  `tfsdk:"extent_size_bytes"`
	MaxVolumeBytes   types.Int64  `tfsdk:"max_volume_bytes"`
	ObservedAt       types.String `tfsdk:"observed_at"`
}

func (r *volumeGroupResource) Metadata(_ context.Context, _ resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = "kouja_volume_group"
}

func (r *volumeGroupResource) Configure(_ context.Context, request resource.ConfigureRequest, response *resource.ConfigureResponse) {
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

func (r *volumeGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, response *resource.SchemaResponse) {
	response.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"name":               schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"registered_vg_uuid": schema.StringAttribute{Computed: true},
		"observed_vg_uuid":   schema.StringAttribute{Computed: true},
		"uuid_drift":         schema.BoolAttribute{Computed: true},
		"state":              schema.StringAttribute{Computed: true},
		"storage_class":      schema.StringAttribute{Computed: true},
		"total_bytes":        schema.Int64Attribute{Computed: true},
		"free_bytes":         schema.Int64Attribute{Computed: true},
		"allocatable_bytes":  schema.Int64Attribute{Computed: true},
		"extent_size_bytes":  schema.Int64Attribute{Computed: true},
		"max_volume_bytes":   schema.Int64Attribute{Computed: true},
		"observed_at":        schema.StringAttribute{Computed: true},
	}}
}

func (r *volumeGroupResource) Create(ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse) {
	var plan volumeGroupModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	registered, err := data.client.RegisterVG(ctx, plan.Name.ValueString(), "", uuid.NewString())
	if err != nil {
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	applyVolumeGroup(&plan, registered.GetCapacity(), plan.Name.ValueString())
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (r *volumeGroupResource) Read(ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse) {
	var state volumeGroupModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	vg, err := data.client.GetVG(ctx, state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			response.State.RemoveResource(ctx)
			return
		}
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	applyVolumeGroupGet(&state, vg)
	if vg.GetUuidDrift() {
		response.Diagnostics.AddWarning("Volume group UUID drift", "stored and observed UUIDs differ; the provider keeps the registered UUID")
	}
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (r *volumeGroupResource) Update(ctx context.Context, _ resource.UpdateRequest, response *resource.UpdateResponse) {
	response.Diagnostics.AddError("Update is not supported", "volume group resources are replaced, not updated")
}

func (r *volumeGroupResource) Delete(ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse) {
	var state volumeGroupModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	data := r.data
	if data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	if err := data.client.DeregisterVG(ctx, state.Name.ValueString(), uuid.NewString()); err != nil && !isNotFound(err) {
		response.Diagnostics.Append(diagnosticFromError(err))
	}
}

func (r *volumeGroupResource) ImportState(ctx context.Context, request resource.ImportStateRequest, response *resource.ImportStateResponse) {
	response.Diagnostics.Append(response.State.SetAttribute(ctx, path.Root("name"), request.ID)...)
}

func applyVolumeGroup(model *volumeGroupModel, capacity *koujav1.Capacity, name string) {
	model.Name = types.StringValue(name)
	model.RegisteredVGUUID = types.StringValue(capacity.GetVgUuid())
	model.ObservedVGUUID = types.StringValue(capacity.GetVgUuid())
	model.UUIDDrift = types.BoolValue(false)
	model.State = types.StringValue(capacity.GetVgState())
	model.StorageClass = types.StringValue(capacity.GetStorageClass())
	model.TotalBytes = types.Int64Value(capacity.GetTotalBytes())
	model.FreeBytes = types.Int64Value(capacity.GetFreeBytes())
	model.AllocatableBytes = types.Int64Value(capacity.GetAllocatableBytes())
	model.ExtentSizeBytes = types.Int64Value(capacity.GetExtentSizeBytes())
	model.MaxVolumeBytes = types.Int64Value(capacity.GetMaxVolumeBytes())
	if capacity.GetObservedAt() != nil {
		model.ObservedAt = types.StringValue(capacity.GetObservedAt().AsTime().UTC().Format(time.RFC3339))
	} else {
		model.ObservedAt = types.StringValue("")
	}
}

func applyVolumeGroupGet(model *volumeGroupModel, vg *koujav1.GetVGResponse) {
	capacity := vg.GetCapacity()
	model.RegisteredVGUUID = types.StringValue(vg.GetRegisteredVgUuid())
	model.ObservedVGUUID = types.StringValue(capacity.GetVgUuid())
	model.UUIDDrift = types.BoolValue(vg.GetUuidDrift())
	model.State = types.StringValue(capacity.GetVgState())
	model.StorageClass = types.StringValue(capacity.GetStorageClass())
	model.TotalBytes = types.Int64Value(capacity.GetTotalBytes())
	model.FreeBytes = types.Int64Value(capacity.GetFreeBytes())
	model.AllocatableBytes = types.Int64Value(capacity.GetAllocatableBytes())
	model.ExtentSizeBytes = types.Int64Value(capacity.GetExtentSizeBytes())
	model.MaxVolumeBytes = types.Int64Value(capacity.GetMaxVolumeBytes())
	if capacity.GetObservedAt() != nil {
		model.ObservedAt = types.StringValue(capacity.GetObservedAt().AsTime().UTC().Format(time.RFC3339))
	}
}
