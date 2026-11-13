package auth

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSoonestResetFirstManagerPreservesForceCooldown(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, credentialScope := range []bool{false, true} {
			t.Run(fmt.Sprintf("affinity=%t/credential=%t", affinity, credentialScope), func(t *testing.T) {
				ctx := WithSkipPersist(t.Context())
				var selector Selector = &SoonestResetFirstSelector{}
				if affinity {
					sticky := NewSessionAffinitySelector(selector)
					t.Cleanup(sticky.Stop)
					selector = sticky
				}
				manager := NewManager(nil, selector, nil)
				cfg := &internalconfig.Config{DisableCooling: true}
				manager.SetConfig(cfg)
				manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "claude"})
				const model = "reset-compat-model"
				now := time.Now()
				soon := resetTestAuth(t.Name()+"-soon", now.Add(time.Hour))
				later := resetTestAuth(t.Name()+"-later", now.Add(24*time.Hour))
				// Weights belong to weighted routing, not reset-based routing.
				soon.Attributes = map[string]string{AttributeWeight: "0"}
				later.Attributes = map[string]string{AttributeWeight: "1000000"}
				for _, auth := range []*Auth{soon, later} {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				pick := func(want string) {
					t.Helper()
					opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": {"force-cooldown-session"}}}
					selected, errPick := manager.SelectAuth(ctx, "claude", model+"(low)", opts)
					if errPick != nil || selected == nil || selected.ID != want {
						t.Fatalf("pick = %v, %v; want %s", selected, errPick, want)
					}
				}
				pick(soon.ID)
				retryAfter := 10 * time.Minute
				manager.MarkResult(ctx, Result{
					AuthID: soon.ID, Provider: "claude", Model: "RESET-COMPAT-MODEL(high)",
					CredentialScope: credentialScope, RetryAfter: &retryAfter,
					Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "forced cooldown"},
				})
				pick(later.ID)
				// Config reload must not clear a forced cooldown, even with cooling off.
				manager.SetConfig(cfg)
				pick(later.ID)
				cooled, ok := manager.GetByID(soon.ID)
				if !ok || IsAuthAvailableForModel(cooled, model+"(low)", time.Now()) {
					t.Fatal("forced cooldown was lost across equivalent model spellings or reload")
				}
				if reset := subscriptionResetTime(cooled, now); !reset.Equal(subscriptionResetTime(soon, now)) {
					t.Fatalf("cooldown changed the passive subscription reset: %v", reset)
				}
			})
		}
	}
}
