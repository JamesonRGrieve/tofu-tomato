// SPDX-License-Identifier: AGPL-3.0-or-later

package tomato

import "testing"

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
