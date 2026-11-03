package management

import "testing"

func TestNormalizeRoutingStrategySoonestResetFirst(t *testing.T) {
	for _, input := range []string{"soonest-reset-first", "soonestresetfirst", " SRF "} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "soonest-reset-first" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v", input, got, ok)
		}
	}
}
