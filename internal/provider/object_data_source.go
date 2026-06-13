// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/JamesonRGrieve/tofu-tomato/internal/tomato"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*nvramDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*nvramDataSource)(nil)
)

// NewObjectDataSource constructs the tomato_nvram data source.
func NewObjectDataSource() datasource.DataSource { return &nvramDataSource{} }

type nvramDataSource struct {
	client *tomato.Client
}

type nvramDataModel struct {
	Key     types.String `tfsdk:"key"`
	Value   types.String `tfsdk:"value"`
	Present types.Bool   `tfsdk:"present"`
	All     types.String `tfsdk:"all"`
}

func (d *nvramDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nvram"
}

func (d *nvramDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Read Tomato NVRAM. Set `key` to read a single variable (`value` + `present`); " +
			"omit `key` to read the full `nvram show` output into `all`.",
		Attributes: map[string]schema.Attribute{
			"key": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "NVRAM variable name to read (e.g. `lan_ipaddr`). Omit to read the whole config into `all`.",
			},
			"value": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The variable's current value (empty string when unset or when reading the whole config).",
			},
			"present": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the variable is defined on the device (false when `key` is omitted).",
			},
			"all": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Full `nvram show` output (every `key=value` line) when `key` is omitted; empty otherwise.",
			},
		},
	}
}

func (d *nvramDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*tomato.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *tomato.Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *nvramDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m nvramDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := strings.TrimSpace(m.Key.ValueString())
	if m.Key.IsNull() || key == "" {
		raw, err := d.client.Show()
		if err != nil {
			resp.Diagnostics.AddError("Tomato nvram show failed", err.Error())
			return
		}
		m.All = types.StringValue(string(raw))
		m.Value = types.StringValue("")
		m.Present = types.BoolValue(false)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	v, present, err := d.client.GetNVRAM(key)
	if err != nil {
		resp.Diagnostics.AddError("Tomato nvram get failed", err.Error())
		return
	}
	m.Value = types.StringValue(v)
	m.Present = types.BoolValue(present)
	m.All = types.StringValue("")
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
