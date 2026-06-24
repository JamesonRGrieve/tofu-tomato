# tomato — Agent Operating Guide

> **⛔ NO DIRECT APPLIES TO ANY DEVICE — EVER.**
>
> Direct changes to **any** device — router, firewall, switch, access point, hypervisor, mail gateway, or any other appliance — are **NEVER** permitted, by anyone, for any reason. This bans hand-run `tofu apply`, hand-run `ansible-playbook`, SSH/serial/CLI config writes, REST/API mutations, and web-GUI/console edits.
>
> **Every change MUST flow through the sanctioned pipeline:** declare intent in **prod-netbox** (the single source of truth), then realize it **only** through **prod-semaphore** (the sanctioned runner). A change that did not go **prod-netbox → prod-semaphore** must never reach a device.
>
> **Sole exception:** a specific direct action is permitted *only* when the operator authorizes that exact action in advance by answering an explicit, **alarm-flavored `AskUserQuestion`** — one that names the device, the precise action, and the risk — **in the affirmative**. No standing grants, no inferred permission, no carrying one approval to another action or device. Absent that in-the-moment "yes," the answer is no.
>
> **Never offload the work onto the operator.** When you are blocked, ask for the break-glass authorization that lets *you* do the job — never ask the operator to run a command, SSH in, or make the change on your behalf. The operator grants permission; they do not perform your labour.

Native OpenTofu/Terraform provider for **Tomato-firmware routers**
(FreshTomato / AdvancedTomato) driven over **SSH**. Sibling of
`../tofu-aruba-aos` and `../openwrt-ubus` (same generic-over-the-device
philosophy, same toolchain). The workspace-root `../CLAUDE.md` applies; this
adds specifics.

## What this is / isn't

- **Is:** a provider for Tomato firmware (FreshTomato / AdvancedTomato — the
  Broadcom MIPS/ARM lineage from Jonathan Zarate's Tomato), managing **NVRAM**
  generically over SSH.
- **Isn't:** an OpenWrt provider (that's `../openwrt-ubus`, ubus-over-HTTP) or a
  REST provider — Tomato has no clean REST API.

## Transport — SSH, and why (decision record)

Tomato keeps all config in **NVRAM**. Two transports were considered:

- **HTTP (httpd):** Basic-auth web UI; writes POST `var=value` form fields to
  `/update.cgi` (a.k.a. `tomato.cgi`) with the CSRF token `_http_id` (scraped
  from a page) plus `_service` (service(s) to restart, e.g. `*`). Writing is
  workable. **Reading is the dealbreaker:** there is no CGI that returns a
  single NVRAM value; status pages embed values inside inline JavaScript, so a
  read means scraping + per-firmware parsing.
- **SSH (Dropbear):** `nvram get <k>` / `nvram set k=v` / `nvram unset k` /
  `nvram commit` / `service <svc> restart`. Reads are exact and structured for
  **any** variable, firmware-independent.

The manage-declared-only subset model **needs** an exact read of each declared
key to compute a 0-diff on import. HTTP cannot give that cleanly; SSH gives it
trivially. **SSH is therefore the strictly cleaner transport for a generic
NVRAM resource — that is the chosen transport.**

We invoke the **system `ssh` binary** via `os/exec` (not an in-process SSH
library). This (a) keeps the module dependency set byte-for-byte unchanged — no
`golang.org/x/crypto/ssh` — per the build constraint, and (b) reuses the lab's
existing SSH machinery: Dropbear key auth or OpenBao-signed SSH certs live in
`ssh_config`/agent exactly as for every other lab host. `ssh -o BatchMode=yes`
ensures it fails fast instead of hanging on a prompt (cf. the prod-lab
"net-routers plan shell SSH hang" lesson).

**Key material — `key_file` vs `key_pem`.** The transport stays a shell-out
either way (go.mod unchanged). `key_file` points ssh at an identity file.
`key_pem` carries the key *material* (e.g. from OpenBao): each call writes it to
a temp 0600 file and removes it afterward. Prefer `key_pem` over pointing
`key_file` at a Terraform-managed `local_sensitive_file` — provider config is
evaluated at **plan**, so the key is present during the refresh/read phase,
whereas a Terraform-written key *file* only exists after **apply**, so a
refresh-time Read fails with `Identity file … No such file`. This is the one
case the provider materializes a private key itself; `key_file`/`ssh_config`
paths still never touch the material.

## Design tenets

- **The generic resource is `tomato_nvram`** (+ data source). `keys` is a JSON
  object of the NVRAM variables managed; everything else in NVRAM is left alone.
- **The subset plan modifier is `nvramSubsetMatches`** — declared keys all match
  device → 0-diff; otherwise the drift surfaces as an update. NVRAM is
  stringly-typed, so values are compared as strings.
- **Restore-on-destroy is exact.** `previous` snapshots each managed key's prior
  value (or absence) at create/import; destroy restores set→value or
  unset→gone, then commits + restarts.
- **`nvram get` cannot distinguish unset from set-empty** (both print nothing),
  so `GetNVRAM` probes `nvram show` for `key=` to return a correct `present`.

## Toolchain

- Go 1.26.4 (`/home/jameson/.local/go`), `terraform-plugin-framework` v1.19.0.
- **Do not add or bump dependencies** — the SSH transport shells out precisely
  so `go.mod` stays unchanged.
- Provider address: `registry.terraform.io/jamesonrgrieve/tomato`. Binary:
  `terraform-provider-tomato`.
- `make check` = tidy + fmt + vet + test + build; `.githooks/pre-commit` re-runs
  the gate. Never `--no-verify`.

## Hard rules

- **No secrets in the repo.** Creds come from the provider config / ssh_config
  (OpenBao-signed certs via the lab's normal SSH path).
- **Lab target:** the only NetBox-modeled Tomato is `tomato64-lab` (device 24,
  platform `tomato`, site Lab) — a placeholder VM (92007 on the gigabyte host,
  100.64.92.7) that is **powered off and "Not a semaphore-target."** Do **not**
  power it on (change-windowed). No production/client Tomato exists in scope.
- Drive any future live changes via Semaphore, plan-first, 0-diff.
