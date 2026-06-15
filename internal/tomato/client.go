// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Package tomato is a minimal NVRAM client for Tomato-firmware routers
// (FreshTomato / AdvancedTomato, the Broadcom/MIPS-and-ARM lineage descended
// from the original Tomato by Jonathan Zarate) driven over SSH (Dropbear).
//
// Why SSH and not HTTP. Tomato has no clean REST API: all configuration lives
// in NVRAM, and the web UI (httpd) writes by POSTing nvram `var=value` form
// fields to /update.cgi with a CSRF token (_http_id) scraped from a page plus a
// _service field naming the service(s) to restart. That write path is usable,
// but the READ path over HTTP is the dealbreaker — there is no CGI that returns
// a single nvram value; the status pages embed values inside inline JavaScript
// that you would have to scrape and parse per-firmware. The manage-declared-only
// subset model (read each declared key's current value, diff it, 0-diff on
// import) needs an exact, structured read of any variable. Over SSH that is just
// `nvram get <key>` — exact, generic, firmware-independent. So SSH is the
// strictly cleaner transport for a generic resource; see CLAUDE.md §"Transport".
//
// We invoke the system `ssh` binary via os/exec rather than an in-process SSH
// library. This keeps the module dependency set unchanged (no golang.org/x/
// crypto/ssh) and reuses the lab's existing SSH machinery — Dropbear key auth or
// OpenBao-signed SSH certificates are configured in the user's ssh_config /
// agent exactly as for every other lab host, so this client never handles a
// private key itself.
package tomato

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// sentinel is emitted by a probe command so we can distinguish an unset NVRAM
// variable (empty output) from a present-but-empty one. `nvram get` prints the
// value with no trailing newline and exits 0 whether or not the key exists, so a
// bare `nvram get k` cannot tell "" (unset) from "" (set to empty). We instead
// ask the shell to report existence explicitly.
const sentinel = "__TOMATO_NVRAM_UNSET__"

// Client runs NVRAM operations on a Tomato router over SSH. It is safe for
// concurrent use; each call spawns its own ssh process. Callers may share one
// Client across resources (the provider does).
type Client struct {
	host    string // host or host:port style is split into addr/port
	addr    string
	port    string
	user    string
	keyFile string // optional explicit identity file (-i)
	sshBin  string
	timeout time.Duration
	// extraArgs are appended to every ssh invocation (e.g. ProxyJump). Mostly
	// for tests; normal use relies on the user's ssh_config.
	extraArgs []string
}

// Config configures a Client.
type Config struct {
	// Host is the router address (host or host:port), no scheme. The default
	// SSH port is 22; Tomato's Dropbear commonly listens on 22 (configurable).
	Host string
	// Username is the SSH user — "root" on Tomato/Dropbear.
	Username string
	// KeyFile is an optional identity file (ssh -i). When empty, the system
	// ssh client resolves the key/agent/cert from ssh_config as usual.
	KeyFile string
	// SSHBinary overrides the ssh executable (default "ssh"). For tests.
	SSHBinary string
	// Timeout per SSH invocation (default 30s).
	Timeout time.Duration
	// ExtraArgs are appended to every ssh command line (e.g. -J jumphost,
	// -o options). Normal deployments leave this empty and use ssh_config.
	ExtraArgs []string
}

// NewClient builds a Client. It does not contact the router until the first
// operation.
func NewClient(c Config) *Client {
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	bin := c.SSHBinary
	if bin == "" {
		bin = "ssh"
	}
	user := c.Username
	if user == "" {
		user = "root"
	}
	addr, port := splitHostPort(c.Host)
	return &Client{
		host:      c.Host,
		addr:      addr,
		port:      port,
		user:      user,
		keyFile:   c.KeyFile,
		sshBin:    bin,
		timeout:   c.Timeout,
		extraArgs: c.ExtraArgs,
	}
}

// splitHostPort splits "host" or "host:port" into (host, port). Port is "" when
// not given (ssh then uses its default / ssh_config).
func splitHostPort(h string) (string, string) {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "ssh://")
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i+1:], "]") {
		// Reject IPv6 without brackets by only treating a trailing :digits as a port.
		if _, err := strconv.Atoi(h[i+1:]); err == nil {
			return h[:i], h[i+1:]
		}
	}
	return h, ""
}

// SSHError is returned when an ssh invocation exits non-zero.
type SSHError struct {
	Cmd      string
	ExitCode int
	Stderr   string
}

func (e *SSHError) Error() string {
	return fmt.Sprintf("tomato ssh %q: exit %d: %s", e.Cmd, e.ExitCode, strings.TrimSpace(e.Stderr))
}

// run executes a single remote shell command over SSH and returns its stdout.
// The remote command is passed as one argument to ssh, which hands it to the
// router's shell — so callers must shell-quote any interpolated values.
func (c *Client) run(remote string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	args := []string{
		"-o", "BatchMode=yes", // never prompt; fail instead of hanging
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeoutSeconds(c.timeout)),
		// Multiplex every per-key nvram op over one SSH connection. A resource's
		// read snapshots many keys (one ssh each) and Tofu applies resources
		// concurrently; without multiplexing the rapid reconnects overwhelm the
		// router's Dropbear ("kex_exchange_identification: Connection reset by
		// peer"). The %C token keys the socket by connection params.
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/tomato-cm-%C",
		"-o", "ControlPersist=20s",
	}
	if c.port != "" {
		args = append(args, "-p", c.port)
	}
	if c.keyFile != "" {
		args = append(args, "-i", c.keyFile, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, c.extraArgs...)
	target := c.addr
	if c.user != "" {
		target = c.user + "@" + c.addr
	}
	args = append(args, target, remote)

	cmd := exec.CommandContext(ctx, c.sshBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if asExit(err, &ee) {
			code = ee.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("tomato ssh %q: timed out after %s", remote, c.timeout)
		}
		return nil, &SSHError{Cmd: remote, ExitCode: code, Stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

// connectTimeoutSeconds derives an ssh ConnectTimeout (>=1s) from the overall
// per-call timeout, leaving headroom for the command itself.
func connectTimeoutSeconds(d time.Duration) int {
	s := int(d.Seconds()) / 2
	if s < 1 {
		s = 1
	}
	return s
}

// GetNVRAM returns the current value of an NVRAM variable and whether it is set.
// `nvram get k` alone cannot distinguish unset from set-empty (both print
// nothing), so we probe existence explicitly: if `nvram get k` is empty we ask
// whether the key appears in `nvram show`. present=false means the variable is
// not defined at all (delete should restore that absence).
func (c *Client) GetNVRAM(key string) (value string, present bool, err error) {
	out, err := c.run(fmt.Sprintf("nvram get %s", shellQuote(key)))
	if err != nil {
		return "", false, err
	}
	// nvram get prints the value with no trailing newline; ssh adds none.
	v := strings.TrimSuffix(string(out), "\n")
	if v != "" {
		return v, true, nil
	}
	// Empty: disambiguate unset vs set-empty.
	probe := fmt.Sprintf("nvram show 2>/dev/null | grep -q %s && echo set || echo %s",
		shellQuote(key+"="), sentinel)
	pout, perr := c.run(probe)
	if perr != nil {
		return "", false, perr
	}
	if strings.Contains(string(pout), sentinel) {
		return "", false, nil
	}
	return "", true, nil
}

// SetNVRAM sets an NVRAM variable to value (in RAM; call Commit to persist).
func (c *Client) SetNVRAM(key, value string) error {
	_, err := c.run(fmt.Sprintf("nvram set %s=%s", shellQuote(key), shellQuote(value)))
	return err
}

// UnsetNVRAM removes an NVRAM variable (in RAM; call Commit to persist).
func (c *Client) UnsetNVRAM(key string) error {
	_, err := c.run(fmt.Sprintf("nvram unset %s", shellQuote(key)))
	return err
}

// Commit persists pending NVRAM changes to flash.
func (c *Client) Commit() error {
	_, err := c.run("nvram commit")
	return err
}

// RestartService restarts a Tomato service (e.g. "wan", "dnsmasq", "firewall").
// A "*" or "all" value restarts everything via `service * restart`. An empty
// service is a no-op (some NVRAM keys take effect only on reboot / are read
// live and need no restart).
func (c *Client) RestartService(service string) error {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil
	}
	_, err := c.run(fmt.Sprintf("service %s restart", shellQuoteService(service)))
	return err
}

// Show returns the full `nvram show` output (every key=value line). Used by the
// data source's whole-config read.
func (c *Client) Show() ([]byte, error) {
	return c.run("nvram show 2>/dev/null")
}

// shellQuote single-quotes s for safe interpolation into a remote /bin/sh
// command line, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellQuoteService quotes a service spec but preserves a bare "*"/"all" so the
// router's `service * restart` form works (the glob must not be quoted away).
func shellQuoteService(s string) string {
	if s == "*" || s == "all" {
		return s
	}
	return shellQuote(s)
}

// asExit reports whether err is an *exec.ExitError and binds it to target.
func asExit(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
