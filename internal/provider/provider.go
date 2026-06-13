// SPDX-License-Identifier: AGPL-3.0-or-later

// Package provider implements the tomato OpenTofu/Terraform provider — a native
// client for Tomato-firmware routers (FreshTomato / AdvancedTomato) over SSH.
// Tomato has no clean REST API; configuration lives in NVRAM, so the provider is
// generic over NVRAM: the tomato_nvram resource/data source manage any NVRAM
// variable (manage-declared-only), giving full coverage without per-feature code.
package provider

import (
	"context"

	"github.com/JamesonRGrieve/tofu-tomato/internal/tomato"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*tomatoProvider)(nil)

// New returns the provider factory for a given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &tomatoProvider{version: version} }
}

type tomatoProvider struct {
	version string
}

type providerModel struct {
	Host      types.String `tfsdk:"host"`
	Username  types.String `tfsdk:"username"`
	KeyFile   types.String `tfsdk:"key_file"`
	SSHBinary types.String `tfsdk:"ssh_binary"`
}

func (p *tomatoProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	// Single-token type name -> resources are `tomato_nvram`, so Terraform's
	// prefix-before-first-underscore inference resolves the local name cleanly
	// (the source address is still jamesonrgrieve/tomato).
	resp.TypeName = "tomato"
	resp.Version = p.version
}

func (p *tomatoProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Native provider for Tomato-firmware routers (FreshTomato / AdvancedTomato) " +
			"driven over SSH (Dropbear). Manages NVRAM variables generically. Tomato has no clean REST " +
			"API, so all config is expressed as NVRAM key/value via the `tomato_nvram` resource.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Router address (host or host:port), no scheme. Default SSH port is 22.",
			},
			"username": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "SSH username (default `root` — Tomato's Dropbear user).",
			},
			"key_file": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Optional SSH identity file (`ssh -i`). When unset, the system ssh " +
					"client resolves the key / agent / OpenBao-signed certificate from ssh_config as usual. " +
					"The provider never handles a private key directly.",
			},
			"ssh_binary": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Path to the ssh executable (default `ssh`).",
			},
		},
	}
}

func (p *tomatoProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	client := tomato.NewClient(tomato.Config{
		Host:      cfg.Host.ValueString(),
		Username:  cfg.Username.ValueString(),
		KeyFile:   cfg.KeyFile.ValueString(),
		SSHBinary: cfg.SSHBinary.ValueString(),
	})
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *tomatoProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewObjectResource}
}

func (p *tomatoProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{NewObjectDataSource}
}
