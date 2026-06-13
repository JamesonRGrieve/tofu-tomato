// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import "testing"

func TestNvramSubsetMatches(t *testing.T) {
	cases := []struct {
		name        string
		prior, cfg  string
		wantMatched bool
	}{
		{
			name:        "config subset of refreshed device keys — match (0-diff)",
			prior:       `{"lan_ipaddr":"192.168.1.1","wan_proto":"dhcp","router_name":"freshtomato"}`,
			cfg:         `{"lan_ipaddr":"192.168.1.1","wan_proto":"dhcp"}`,
			wantMatched: true,
		},
		{
			name:        "declared key drifted — no match (update)",
			prior:       `{"lan_ipaddr":"192.168.1.254","wan_proto":"dhcp"}`,
			cfg:         `{"lan_ipaddr":"192.168.1.1"}`,
			wantMatched: false,
		},
		{
			name:        "declared key absent on device — no match",
			prior:       `{"wan_proto":"dhcp"}`,
			cfg:         `{"lan_ipaddr":"192.168.1.1"}`,
			wantMatched: false,
		},
		{
			name:        "key order / whitespace insensitive — match",
			prior:       `{"b":"2","a":"1"}`,
			cfg:         "{\n  \"a\": \"1\",\n  \"b\": \"2\"\n}",
			wantMatched: true,
		},
		{
			name:        "empty config is a trivial subset — match",
			prior:       `{"a":"1"}`,
			cfg:         `{}`,
			wantMatched: true,
		},
		{
			name:        "invalid prior JSON — no match (fall back to diff)",
			prior:       `not json`,
			cfg:         `{"a":"1"}`,
			wantMatched: false,
		},
		{
			name:        "non-string declared value — no match (parse error)",
			prior:       `{"a":"1"}`,
			cfg:         `{"a":1}`,
			wantMatched: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nvramSubsetMatches(tc.prior, tc.cfg); got != tc.wantMatched {
				t.Fatalf("nvramSubsetMatches() = %v, want %v", got, tc.wantMatched)
			}
		})
	}
}

func TestParseKeyMap(t *testing.T) {
	t.Run("valid string map", func(t *testing.T) {
		m, err := parseKeyMap(`{"lan_ipaddr":"192.168.1.1","wan_proto":"dhcp"}`)
		if err != nil {
			t.Fatal(err)
		}
		if m["lan_ipaddr"] != "192.168.1.1" || m["wan_proto"] != "dhcp" || len(m) != 2 {
			t.Fatalf("parseKeyMap got %#v", m)
		}
	})
	t.Run("empty string is empty map", func(t *testing.T) {
		m, err := parseKeyMap("")
		if err != nil || len(m) != 0 {
			t.Fatalf("parseKeyMap(\"\") = %#v, err=%v", m, err)
		}
	})
	t.Run("non-string value errors", func(t *testing.T) {
		if _, err := parseKeyMap(`{"a":1}`); err == nil {
			t.Fatal("expected error for non-string value")
		}
	})
	t.Run("non-object errors", func(t *testing.T) {
		if _, err := parseKeyMap(`[1,2,3]`); err == nil {
			t.Fatal("expected error for non-object")
		}
	})
}

func TestMarshalKeyMapSorted(t *testing.T) {
	got := marshalKeyMap(map[string]string{"b": "2", "a": "1"})
	if got != `{"a":"1","b":"2"}` {
		t.Fatalf("marshalKeyMap = %q", got)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	set := "192.168.1.1"
	snap := map[string]*string{
		"lan_ipaddr": &set, // present with value
		"new_key":    nil,  // did not exist
	}
	enc := marshalSnapshot(snap)
	if enc != `{"lan_ipaddr":"192.168.1.1","new_key":null}` {
		t.Fatalf("marshalSnapshot = %q", enc)
	}
	dec, err := parseSnapshot(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec["lan_ipaddr"] == nil || *dec["lan_ipaddr"] != "192.168.1.1" {
		t.Fatalf("lan_ipaddr round-trip wrong: %#v", dec["lan_ipaddr"])
	}
	if dec["new_key"] != nil {
		t.Fatalf("new_key should decode to nil, got %#v", dec["new_key"])
	}
}

func TestSnapEntry(t *testing.T) {
	if e := snapEntry("", false); e != nil {
		t.Fatalf("absent key should snap to nil, got %v", *e)
	}
	if e := snapEntry("", true); e == nil || *e != "" {
		t.Fatalf("present-empty key should snap to non-nil empty string, got %v", e)
	}
	if e := snapEntry("v", true); e == nil || *e != "v" {
		t.Fatalf("present key should snap to its value, got %v", e)
	}
}

func TestKeyID(t *testing.T) {
	got := keyID(map[string]string{"wan_proto": "dhcp", "lan_ipaddr": "x"})
	if got != "lan_ipaddr|wan_proto" {
		t.Fatalf("keyID = %q", got)
	}
}

func TestSplitKeys(t *testing.T) {
	cases := map[string][]string{
		"a|b|c":      {"a", "b", "c"},
		" a | b ||c": {"a", "b", "c"},
		"":           nil,
		"only":       {"only"},
	}
	for in, want := range cases {
		got := splitKeys(in)
		if len(got) != len(want) {
			t.Fatalf("splitKeys(%q) = %v, want %v", in, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("splitKeys(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestDeclaredSet(t *testing.T) {
	s := declaredSet([]string{"a", "b"})
	if _, ok := s["a"]; !ok {
		t.Fatal("declaredSet missing a")
	}
	if _, ok := s["b"]; !ok {
		t.Fatal("declaredSet missing b")
	}
	if len(s) != 2 {
		t.Fatalf("declaredSet len = %d", len(s))
	}
}
