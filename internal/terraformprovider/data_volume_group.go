package terraformprovider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type volumeGroupDataSource struct {
	data *providerData
}

func (d *volumeGroupDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, response *datasource.MetadataResponse) {
	response.TypeName = "kouja_volume_group"
}

func (d *volumeGroupDataSource) Configure(_ context.Context, request datasource.ConfigureRequest, response *datasource.ConfigureResponse) {
	if request.ProviderData == nil {
		return
	}
	data, ok := request.ProviderData.(*providerData)
	if !ok {
		response.Diagnostics.AddError("Unexpected provider data", "expected *providerData")
		return
	}
	d.data = data
}

func (d *volumeGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, response *datasource.SchemaResponse) {
	response.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"name":               schema.StringAttribute{Required: true},
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

func (d *volumeGroupDataSource) Read(ctx context.Context, request datasource.ReadRequest, response *datasource.ReadResponse) {
	var model volumeGroupModel
	response.Diagnostics.Append(request.Config.Get(ctx, &model)...)
	if response.Diagnostics.HasError() {
		return
	}
	if d.data == nil {
		response.Diagnostics.AddError("Provider is not configured", "missing provider data")
		return
	}
	vg, err := d.data.client.GetVG(ctx, model.Name.ValueString())
	if err != nil {
		response.Diagnostics.Append(diagnosticFromError(err))
		return
	}
	applyVolumeGroupGet(&model, vg)
	model.Name = types.StringValue(model.Name.ValueString())
	response.Diagnostics.Append(response.State.Set(ctx, &model)...)
}
