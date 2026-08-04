package mcpruntime

import "testing"

func TestProtocolUsesConnectionToolCacheOnlyForLegacyServers(t *testing.T) {
	for _, test := range []struct {
		protocolVersion string
		want            bool
	}{
		{protocolVersion: "2025-11-25", want: true},
		{protocolVersion: modernProtocolVersion, want: false},
		{protocolVersion: "2027-01-01", want: false},
	} {
		if got := protocolUsesConnectionToolCache(test.protocolVersion); got != test.want {
			t.Errorf(
				"protocolUsesConnectionToolCache(%q) = %t, want %t",
				test.protocolVersion,
				got,
				test.want,
			)
		}
	}
}
