// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/JamesonRGrieve/tofu-tomato/internal/tomato"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*nvramResource)(nil)
	_ resource.ResourceWithConfigure   = (*nvramResource)(nil)
	_ resource.ResourceWithImportState = (*nvramResource)(nil)
)

// NewObjectResource constructs the generic tomato_nvram resource.
func NewObjectResource() resource.Resource { return &nvramResource{} }

type nvramResource struct {
	client *tomato.Client
}

// nvramModel is the state/plan shape for tomato_nvram.
//
//   - Keys     — the managed key=value map, declared as a JSON object. These are
//     the ONLY NVRAM variables this resource touches.
//   - Restart  — service(s) to restart after commit ("wan", "dnsmasq", "*", …);
//     empty means no restart.
//   - Previous — computed snapshot of each managed key's value (or absence)
//     captured at create/import, used to restore on destroy.
//   - ID       — pipe-joined sorted key names, stable per managed set.
type nvramModel struct {
	ID       types.String `tfsdk:"id"`
	Keys     types.String `tfsdk:"keys"`
	Restart  types.String `tfsdk:"restart"`
	Previous types.String `tfsdk:"previous"`
}

func (r *nvramResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nvram"
}

func (r *nvramResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A generic Tomato (FreshTomato / AdvancedTomato) NVRAM resource. " +
			"`keys` is a JSON object of the NVRAM variables this resource manages — any variable " +
			"is expressible. On create/update the declared keys are `nvram set`, then `nvram commit`, " +
			"then the `restart` service is restarted. **Manage-declared-only:** only the keys in " +
			"`keys` are ever touched; every other NVRAM variable is left alone. A subset/no-op plan " +
			"modifier suppresses the diff when every declared key already holds its declared value on " +
			"the device, so an existing config imports to 0-diff and unmanaged NVRAM is never clobbered. " +
			"On destroy, each managed key is restored to the value it had at create/import (or unset if " +
			"it did not exist), then committed and restarted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource id — the managed key names, sorted and pipe-joined.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"keys": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "JSON object mapping NVRAM variable name → value, e.g. " +
					"`jsonencode({ lan_ipaddr = \"192.168.1.1\", wan_proto = \"dhcp\" })`. " +
					"Only these variables are managed; all values are strings (NVRAM is stringly-typed).",
				PlanModifiers: []planmodifier.String{nvramSubsetSuppress{}},
			},
			"restart": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Service(s) to `service <svc> restart` after committing — e.g. " +
					"`wan`, `lan`, `dnsmasq`, `firewall`, `httpd`, or `*` for all. Omit / empty for keys " +
					"that need no restart (read live or reboot-only).",
			},
			"previous": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Computed snapshot of each managed key's prior value, captured at " +
					"create/import. JSON object: a string value means the key existed with that value; " +
					"`null` means it did not exist. Used to restore exactly on destroy.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *nvramResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*tomato.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("expected *tomato.Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *nvramResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m nvramModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	declared, err := parseKeyMap(m.Keys.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid keys", err.Error())
		return
	}
	// Snapshot prior values so destroy can restore exactly.
	prev, err := r.snapshot(declared)
	if err != nil {
		resp.Diagnostics.AddError("Tomato read (snapshot) failed", err.Error())
		return
	}
	if err := r.applyKeys(declared, m.Restart); err != nil {
		resp.Diagnostics.AddError("Tomato set/commit failed", err.Error())
		return
	}
	m.ID = types.StringValue(keyID(declared))
	m.Previous = types.StringValue(prev)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *nvramResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m nvramModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	declared, err := parseKeyMap(m.Keys.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid keys in state", err.Error())
		return
	}
	// Refresh each managed key's current device value into `keys`. The subset
	// plan modifier reconciles this against the config at plan time, so a value
	// that still matches config shows 0-diff and a drifted value shows an update.
	current := make(map[string]string, len(declared))
	for k := range declared {
		v, present, gerr := r.client.GetNVRAM(k)
		if gerr != nil {
			resp.Diagnostics.AddError("Tomato read failed", gerr.Error())
			return
		}
		if !present {
			// Key vanished on the device — drop it from the refreshed map so the
			// diff surfaces (config will want to re-set it).
			continue
		}
		current[k] = v
	}
	m.Keys = types.StringValue(marshalKeyMap(current))
	m.ID = types.StringValue(keyID(declared))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *nvramResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state nvramModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	declared, err := parseKeyMap(plan.Keys.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid keys", err.Error())
		return
	}
	prevState, _ := parseSnapshot(state.Previous.ValueString())
	oldDeclared, _ := parseKeyMap(state.Keys.ValueString())

	// Any key managed before but no longer declared is restored to its captured
	// prior value (or unset) — it leaves management cleanly.
	for k := range oldDeclared {
		if _, still := declared[k]; still {
			continue
		}
		if err := r.restoreKey(k, prevState); err != nil {
			resp.Diagnostics.AddError("Tomato restore (dropped key) failed", err.Error())
			return
		}
	}
	// Newly-declared keys get a snapshot entry captured now; keep existing ones.
	merged := prevState
	for k := range declared {
		if _, ok := merged[k]; ok {
			continue
		}
		v, present, gerr := r.client.GetNVRAM(k)
		if gerr != nil {
			resp.Diagnostics.AddError("Tomato read (snapshot) failed", gerr.Error())
			return
		}
		merged[k] = snapEntry(v, present)
	}
	// Prune snapshot entries for keys no longer managed.
	for k := range merged {
		if _, ok := declared[k]; !ok {
			delete(merged, k)
		}
	}
	if err := r.applyKeys(declared, plan.Restart); err != nil {
		resp.Diagnostics.AddError("Tomato set/commit failed", err.Error())
		return
	}
	plan.ID = types.StringValue(keyID(declared))
	plan.Previous = types.StringValue(marshalSnapshot(merged))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *nvramResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m nvramModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	declared, err := parseKeyMap(m.Keys.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid keys in state", err.Error())
		return
	}
	prev, _ := parseSnapshot(m.Previous.ValueString())
	for k := range declared {
		if err := r.restoreKey(k, prev); err != nil {
			resp.Diagnostics.AddError("Tomato restore failed", err.Error())
			return
		}
	}
	if err := r.client.Commit(); err != nil {
		resp.Diagnostics.AddError("Tomato commit failed", err.Error())
		return
	}
	if err := r.client.RestartService(m.Restart.ValueString()); err != nil {
		resp.Diagnostics.AddError("Tomato service restart failed", err.Error())
	}
}

func (r *nvramResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import id is a pipe-delimited list of NVRAM key names, optionally with a
	// trailing "@restart" suffix on the LAST field naming the restart service so
	// the imported state matches config:
	//   lan_ipaddr|lan_netmask|wan_proto@wan
	// The following Read populates `keys` with the live values and `previous` is
	// captured here from the live device.
	raw := strings.TrimSpace(req.ID)
	restart := ""
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		restart = raw[i+1:]
		raw = raw[:i]
	}
	names := splitKeys(raw)
	if len(names) == 0 {
		resp.Diagnostics.AddError("Invalid import id",
			"expected pipe-delimited NVRAM key names, optionally suffixed with @<restart-service>")
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "import requires a configured provider client")
		return
	}
	keys := make(map[string]string, len(names))
	snap := make(map[string]*string, len(names))
	for _, k := range names {
		v, present, err := r.client.GetNVRAM(k)
		if err != nil {
			resp.Diagnostics.AddError("Tomato import read failed", err.Error())
			return
		}
		if present {
			keys[k] = v
		}
		snap[k] = snapEntry(v, present)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), keyID(declaredSet(names)))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("keys"), marshalKeyMap(keys))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("previous"), marshalSnapshot(snap))...)
	if restart != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("restart"), restart)...)
	} else {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("restart"), types.StringNull())...)
	}
}

// applyKeys sets every declared key, commits, then restarts the named service.
func (r *nvramResource) applyKeys(declared map[string]string, restart types.String) error {
	// Deterministic order keeps behavior reproducible and tests stable.
	for _, k := range sortedKeys(declared) {
		if err := r.client.SetNVRAM(k, declared[k]); err != nil {
			return err
		}
	}
	if err := r.client.Commit(); err != nil {
		return err
	}
	return r.client.RestartService(restart.ValueString())
}

// snapshot captures the current value/absence of every declared key as a JSON
// snapshot object for restore-on-destroy.
func (r *nvramResource) snapshot(declared map[string]string) (string, error) {
	snap := make(map[string]*string, len(declared))
	for k := range declared {
		v, present, err := r.client.GetNVRAM(k)
		if err != nil {
			return "", err
		}
		snap[k] = snapEntry(v, present)
	}
	return marshalSnapshot(snap), nil
}

// restoreKey restores a single key to its snapshot state: set to the captured
// value, or unset if it did not previously exist (or has no snapshot entry).
func (r *nvramResource) restoreKey(k string, snap map[string]*string) error {
	if v, ok := snap[k]; ok && v != nil {
		return r.client.SetNVRAM(k, *v)
	}
	return r.client.UnsetNVRAM(k)
}

// ---------------------------------------------------------------------------
// Pure helpers — JSON key map + snapshot encoding, id derivation. Unit-tested.
// ---------------------------------------------------------------------------

// parseKeyMap parses the `keys` JSON object into a string→string map. All values
// must be JSON strings (NVRAM is stringly-typed); a non-string value is an error.
func parseKeyMap(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return map[string]string{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("`keys` must be a JSON object: %w", err)
	}
	out := make(map[string]string, len(raw))
	for k, rv := range raw {
		var v string
		if err := json.Unmarshal(rv, &v); err != nil {
			return nil, fmt.Errorf("key %q: NVRAM values must be strings, got %s", k, string(rv))
		}
		out[k] = v
	}
	return out, nil
}

// marshalKeyMap serializes a key map as a compact, key-sorted JSON object.
func marshalKeyMap(m map[string]string) string {
	out, err := json.Marshal(m) // map marshalling sorts keys
	if err != nil {
		return "{}"
	}
	return string(out)
}

// parseSnapshot parses a snapshot object: each value is a JSON string (key
// existed with that value) or null (key did not exist).
func parseSnapshot(s string) (map[string]*string, error) {
	out := map[string]*string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// marshalSnapshot serializes a snapshot map as compact, key-sorted JSON.
func marshalSnapshot(m map[string]*string) string {
	out, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// snapEntry builds a snapshot entry from a (value, present) read result.
func snapEntry(v string, present bool) *string {
	if !present {
		return nil
	}
	val := v
	return &val
}

// keyID derives the resource id from the managed key set: sorted, pipe-joined.
func keyID(m map[string]string) string {
	return strings.Join(sortedKeys(m), "|")
}

// declaredSet turns a name slice into a set-shaped map (values irrelevant).
func declaredSet(names []string) map[string]string {
	m := make(map[string]string, len(names))
	for _, n := range names {
		m[n] = ""
	}
	return m
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// splitKeys splits a pipe-delimited key list, trimming and dropping empties.
func splitKeys(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "|") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// subset / no-op plan modifier — suppress the diff on `keys` when every declared
// key already holds its declared value on the device (the refreshed state held
// in prior state). This is what lets a declared NVRAM subset import/refresh to
// 0-diff without ever touching unmanaged variables.
// ---------------------------------------------------------------------------

type nvramSubsetSuppress struct{}

func (nvramSubsetSuppress) Description(context.Context) string {
	return "Suppress diff when every declared NVRAM key already holds its declared value on the device."
}
func (nvramSubsetSuppress) MarkdownDescription(context.Context) string {
	return (nvramSubsetSuppress{}).Description(nil)
}

func (nvramSubsetSuppress) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return // create — nothing to reconcile against
	}
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	// Prior state holds the device's current managed-key values (refreshed by
	// Read). If every declared (config) key already matches, keep prior state and
	// show no diff; otherwise leave the config value so the drift is an update.
	if nvramSubsetMatches(req.StateValue.ValueString(), req.ConfigValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}

// nvramSubsetMatches reports whether every key in the config object is present
// in the prior object with an equal value (config is a value-subset of prior).
// Invalid JSON on either side returns false so the caller falls back to a diff.
func nvramSubsetMatches(prior, cfg string) bool {
	p, err := parseKeyMap(prior)
	if err != nil {
		return false
	}
	c, err := parseKeyMap(cfg)
	if err != nil {
		return false
	}
	for k, cv := range c {
		pv, ok := p[k]
		if !ok || pv != cv {
			return false
		}
	}
	return true
}
