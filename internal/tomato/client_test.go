// SPDX-License-Identifier: AGPL-3.0-or-later

package tomato

import (
	"os"
	"testing"
)

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in         string
		host, port string
	}{
		{"192.168.1.1", "192.168.1.1", ""},
		{"192.168.1.1:2222", "192.168.1.1", "2222"},
		{"ssh://router:22", "router", "22"},
		{" router ", "router", ""},
		{"router:notaport", "router:notaport", ""},
	}
	for _, tc := range cases {
		h, p := splitHostPort(tc.in)
		if h != tc.host || p != tc.port {
			t.Errorf("splitHostPort(%q) = (%q,%q), want (%q,%q)", tc.in, h, p, tc.host, tc.port)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":          "'plain'",
		"with space":     "'with space'",
		"it's":           `'it'\''s'`,
		"a=b;rm -rf /":   "'a=b;rm -rf /'",
		"$(whoami)":      "'$(whoami)'",
		"back`tick`":     "'back`tick`'",
		`"double"`:       `'"double"'`,
		"192.168.1.1/24": "'192.168.1.1/24'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellQuoteService(t *testing.T) {
	if got := shellQuoteService("*"); got != "*" {
		t.Errorf("shellQuoteService(*) = %q, want * (unquoted)", got)
	}
	if got := shellQuoteService("all"); got != "all" {
		t.Errorf("shellQuoteService(all) = %q, want all (unquoted)", got)
	}
	if got := shellQuoteService("wan"); got != "'wan'" {
		t.Errorf("shellQuoteService(wan) = %q, want 'wan'", got)
	}
}

func TestConnectTimeoutSeconds(t *testing.T) {
	if got := connectTimeoutSeconds(0); got != 1 {
		t.Errorf("connectTimeoutSeconds(0) = %d, want 1 (floor)", got)
	}
	if got := connectTimeoutSeconds(30e9); got != 15 {
		t.Errorf("connectTimeoutSeconds(30s) = %d, want 15", got)
	}
}

func TestNewClientDefaults(t *testing.T) {
	c := NewClient(Config{Host: "10.0.0.1"})
	if c.user != "root" {
		t.Errorf("default user = %q, want root", c.user)
	}
	if c.sshBin != "ssh" {
		t.Errorf("default ssh binary = %q, want ssh", c.sshBin)
	}
	if c.addr != "10.0.0.1" || c.port != "" {
		t.Errorf("addr/port = %q/%q", c.addr, c.port)
	}
}

func TestTransientSSH(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"kex reset", &SSHError{Stderr: "kex_exchange_identification: read: Connection reset by peer"}, true},
		{"connection reset by host", &SSHError{Stderr: "Connection reset by 10.0.0.1 port 22"}, true},
		{"connection closed by host", &SSHError{Stderr: "Connection closed by 10.0.0.1 port 22"}, true},
		{"real command error", &SSHError{ExitCode: 1, Stderr: "nvram: not found"}, false},
		{"non-ssh error", errStub{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transientSSH(tc.err); got != tc.want {
				t.Fatalf("transientSSH() = %v, want %v", got, tc.want)
			}
		})
	}
}

type errStub struct{}

func (errStub) Error() string { return "boom" }

func TestIdentityFileExplicitKeyFileWins(t *testing.T) {
	c := NewClient(Config{Host: "10.0.0.1", KeyFile: "/path/to/id", KeyPEM: "ignored"})
	path, cleanup, err := c.identityFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if path != "/path/to/id" {
		t.Fatalf("path = %q, want explicit key_file", path)
	}
}

func TestIdentityFileNoneFallsBackToSSHConfig(t *testing.T) {
	c := NewClient(Config{Host: "10.0.0.1"})
	path, cleanup, err := c.identityFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if path != "" {
		t.Fatalf("path = %q, want empty (ssh_config fallback)", path)
	}
}

func TestIdentityFileMaterializesKeyPEM(t *testing.T) {
	const pem = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----"
	c := NewClient(Config{Host: "10.0.0.1", KeyPEM: pem})
	path, cleanup, err := c.identityFile()
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("expected a temp identity path")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("temp key not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("temp key perm = %o, want 600", perm)
	}
	got, err := os.ReadFile(path) //nolint:gosec // test reads the temp file it just wrote
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pem+"\n" {
		t.Fatalf("temp key content = %q, want pem + trailing newline", string(got))
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove temp key: %v", err)
	}
}
