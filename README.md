<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# terraform-provider-tomato

A native OpenTofu/Terraform provider for **Tomato-firmware routers** —
**FreshTomato** and **AdvancedTomato** (the Broadcom-based MIPS/ARM lineage
descended from Jonathan Zarate's original Tomato) — driven over **SSH**
(Dropbear). It manages router configuration generically as **NVRAM** key/value.

## Why NVRAM, and why over SSH

Tomato has **no clean REST API**. All configuration lives in **NVRAM**
(`router_name`, `lan_ipaddr`, `wan_proto`, `dhcpd_*`, `dnsmasq_custom`, the
`wl_*` wireless variables, port-forward / firewall script vars, …). There are
two ways to drive it:

- **HTTP (httpd).** The web UI POSTs `var=value` form fields to `/update.cgi`
  with a CSRF token (`_http_id`, scraped from a page) plus a `_service` field
  naming the service(s) to restart. Writing is workable, but **reading is the
  dealbreaker** — no CGI returns a single NVRAM value; the status pages embed
  values inside inline JavaScript that you would scrape and parse per-firmware.
- **SSH (Dropbear).** `nvram get <k>` / `nvram set k=v` / `nvram commit` /
  `service <svc> restart`. Reads are exact and structured for **any** variable,
  firmware-independent.

The manage-declared-only subset model needs an exact read of each declared key
to compute a 0-diff on import — which HTTP cannot give cleanly but SSH gives
trivially. **SSH is the strictly cleaner transport for a generic resource**, so
this provider uses it. (See `CLAUDE.md` for the full rationale.) The provider
shells out to the system `ssh` client, so it never handles a private key itself
and adds no Go SSH dependency — key / agent / OpenBao-signed-cert auth is
configured in your `ssh_config` exactly as for any other host.

## Resources

### `tomato_nvram` (resource)

Manages a declared set of NVRAM variables. CRUD + `ImportState`.

```hcl
resource "tomato_nvram" "lan" {
  keys = jsonencode({
    lan_ipaddr  = "192.168.1.1"
    lan_netmask = "255.255.255.0"
  })
  restart = "lan"            # service <svc> restart after commit
}

resource "tomato_nvram" "identity" {
  keys    = jsonencode({ router_name = "freshtomato", wan_hostname = "ft" })
  restart = "httpd"
}
```

On **create/update**: every key in `keys` is `nvram set`, then `nvram commit`,
then `service <restart> restart`. On **destroy**: each managed key is restored
to the value it had at create/import (or `nvram unset` if it did not previously
exist), then committed and restarted.

**Manage-declared-only / 0-diff imports.** `keys` declares *only* the variables
you manage. The provider touches *nothing* else in NVRAM. A plan modifier
suppresses the diff when every declared key already holds its declared value on
the device, so:

- importing an existing config (`tofu import` / `import {}`) lands at
  **0-diff** with no apply against the router, and
- unmanaged NVRAM is never clobbered.

| Attribute | | Meaning |
|-----------|---|---------|
| `keys` | required | JSON object of NVRAM `name → value` (all values are strings) |
| `restart` | optional | service(s) to `service <svc> restart` after commit — `wan`, `lan`, `dnsmasq`, `firewall`, `httpd`, `*` for all; omit for none |
| `previous` | computed | snapshot of each key's prior value (or `null` = did not exist), used to restore on destroy |
| `id` | computed | the managed key names, sorted and pipe-joined |

**Import id** is a pipe-delimited list of key names, with an optional
`@<restart-service>` suffix on the last field:

```sh
tofu import tomato_nvram.lan 'lan_ipaddr|lan_netmask@lan'
```

### `tomato_nvram` (data source)

```hcl
data "tomato_nvram" "lan_ip" { key = "lan_ipaddr" }  # .value + .present
data "tomato_nvram" "all"    {}                       # .all = full `nvram show`
```

## Provider configuration

```hcl
terraform {
  required_providers {
    tomato = { source = "registry.terraform.io/jamesonrgrieve/tomato" }
  }
}

provider "tomato" {
  host       = "192.168.1.1"   # host or host:port, no scheme (default SSH port 22)
  username   = "root"          # optional, default root (Dropbear)
  key_file   = var.ssh_key     # optional; omit to use ssh_config / agent / cert
  ssh_binary = "ssh"           # optional, default ssh
}
```

The router's Dropbear SSH must be enabled (Administration → Admin Access), and
the user's SSH key / agent / signed certificate must be configured in
`ssh_config` (the provider runs `ssh -o BatchMode=yes`, so it fails fast rather
than prompting).

## Local build / dev install

```sh
make build          # -> terraform-provider-tomato
make install        # installs to $DEV_BIN_DIR for a dev_overrides .tfrc
make check          # tidy + fmt + vet + test + build (pre-commit / CI gate)
```

For runners without registry access, install into a filesystem mirror:
`<plugins>/registry.terraform.io/JamesonRGrieve/tofu-tomato/<ver>/<os>_<arch>/terraform-provider-tomato`
and point a `.terraformrc` `provider_installation { filesystem_mirror {...} }` at it.

## License

AGPL-3.0-or-later.
