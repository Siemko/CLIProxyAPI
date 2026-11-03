package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSoonestResetFirstRoutingSelector(t *testing.T) {
	for _, strategy := range []string{"soonest-reset-first", "soonestresetfirst", " SRF "} {
		cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: strategy}}
		state := normalizedRoutingRuntimeState(cfg)
		if state.strategy != "soonest-reset-first" {
			t.Fatalf("strategy = %q", state.strategy)
		}
		if _, ok := newRoutingSelector(state).(*coreauth.SoonestResetFirstSelector); !ok {
			t.Fatalf("selector = %T", newRoutingSelector(state))
		}
		cfg.Routing.SessionAffinity = true
		selector := newRoutingSelector(normalizedRoutingRuntimeState(cfg))
		if affinity, ok := selector.(*coreauth.SessionAffinitySelector); !ok {
			t.Fatalf("affinity selector = %T", selector)
		} else {
			affinity.Stop()
		}
	}
}
