// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"errors"
	"testing"
)

func TestRunReconcile(t *testing.T) {
	tests := []struct {
		name     string
		services []string
		restart  func(string) error
		wantWarn int
		wantAll  bool
	}{
		{
			name:     "all ok",
			services: []string{"firewall", "dnsmasq"},
			restart:  func(string) error { return nil },
			wantWarn: 0, wantAll: false,
		},
		{
			name:     "no services is a no-op",
			services: nil,
			restart:  func(string) error { return errors.New("should not be called") },
			wantWarn: 0, wantAll: false,
		},
		{
			name:     "ssh error on the only service escalates",
			services: []string{"firewall"},
			restart:  func(string) error { return errors.New("tomato ssh: exit 255: connection refused") },
			wantWarn: 1, wantAll: true,
		},
		{
			name:     "partial failure warns but does not escalate",
			services: []string{"firewall", "dnsmasq"},
			restart: func(s string) error {
				if s == "dnsmasq" {
					return errors.New("tomato ssh: exit 1: unknown service") // service absent on this box
				}
				return nil
			},
			wantWarn: 1, wantAll: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warns, all := runReconcile(tt.services, tt.restart)
			if len(warns) != tt.wantWarn {
				t.Errorf("warnings = %d (%v), want %d", len(warns), warns, tt.wantWarn)
			}
			if all != tt.wantAll {
				t.Errorf("allFailed = %v, want %v", all, tt.wantAll)
			}
		})
	}
}
