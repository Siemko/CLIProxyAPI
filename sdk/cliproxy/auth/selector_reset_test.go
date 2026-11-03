package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func resetTestAuth(id string, reset time.Time) *Auth {
	return &Auth{ID: id, Provider: "claude", Status: StatusActive, Quota: QuotaState{
		Signals: map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(reset.Unix(), 10)},
	}}
}

func TestSubscriptionResetTime(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	soon, later := now.Add(time.Hour), now.Add(24*time.Hour)
	epoch := func(at time.Time) string { return strconv.FormatInt(at.Unix(), 10) }
	for _, tt := range []struct {
		name, provider string
		signals        map[string]string
		observed       time.Time
		want           time.Time
	}{
		{"claude weekly before five-hour", "claude", map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": epoch(later), "Anthropic-Ratelimit-Unified-5h-Reset": epoch(soon)}, now, later},
		{"claude short-only", "claude", map[string]string{"Anthropic-Ratelimit-Unified-5h-Reset": soon.Format(time.RFC3339)}, now, soon},
		{"claude stale weekly is unknown", "claude", map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": epoch(now), "Anthropic-Ratelimit-Unified-5h-Reset": epoch(soon)}, now, time.Time{}},
		{"invalid", "claude", map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": "garbage"}, now, time.Time{}},
		{"codex weekly primary", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": epoch(later), "X-Codex-Secondary-Window-Minutes": "300", "X-Codex-Secondary-Reset-At": epoch(soon)}, now, later},
		{"codex weekly secondary", "codex", map[string]string{"X-Codex-Secondary-Window-Minutes": "10080", "X-Codex-Secondary-Reset-At": epoch(later), "X-Codex-Primary-Window-Minutes": "300", "X-Codex-Primary-Reset-At": epoch(soon)}, now, later},
		{"codex relative anchored to observation", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "7200"}, now.Add(-time.Hour), soon},
		{"codex relative without observation", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "7200"}, time.Time{}, time.Time{}},
		{"codex expired relative", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "1"}, now.Add(-time.Hour), time.Time{}},
		{"codex overflowing relative", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "9223372036854775807"}, now, time.Time{}},
		{"codex ignores unrelated quota", "codex", map[string]string{"X-Codex-Code-Review-Primary-Window-Minutes": "10080", "X-Codex-Code-Review-Primary-Reset-At": epoch(soon)}, now, time.Time{}},
		{"devin weekly", "devin", map[string]string{"weekly_quota_reset_at": later.Format(time.RFC3339), "daily_quota_reset_at": soon.Format(time.RFC3339)}, now, later},
		{"unknown provider", "gemini", map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": epoch(soon)}, now, time.Time{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &Auth{Provider: tt.provider, Quota: QuotaState{Signals: tt.signals, ObservedAt: tt.observed}}
			if got := subscriptionResetTime(a, now); !got.Equal(tt.want) {
				t.Fatalf("reset = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSoonestResetFirstSelectorOrderingAndDiscovery(t *testing.T) {
	now := time.Now()
	s := &SoonestResetFirstSelector{nowFunc: func() time.Time { return now }}
	later := resetTestAuth("a-later", now.Add(4*24*time.Hour))
	soon := resetTestAuth("z-soon", now.Add(24*time.Hour))
	unknown := &Auth{ID: "unknown", Provider: "claude"}
	available := []*Auth{later, soon}
	pick := func(want string) {
		t.Helper()
		got, err := s.Pick(context.Background(), "claude", "model", cliproxyexecutor.Options{}, available)
		if err != nil || got.ID != want {
			t.Fatalf("pick = %v, %v; want %s", got, err, want)
		}
	}
	for range 10 {
		pick(soon.ID)
	}
	available = append(available, unknown)
	pick(unknown.ID)
	pick(soon.ID)
	now = now.Add(resetDiscoveryInterval)
	pick(unknown.ID)
	pick(soon.ID)
	unknown.Quota = resetTestAuth("", now.Add(time.Hour)).Quota
	pick(unknown.ID)
	unknown.Disabled = true
	pick(soon.ID)
	soon.Unavailable, soon.NextRetryAfter = true, now.Add(time.Hour)
	soon.Quota.Exceeded, soon.Quota.NextRecoverAt = true, soon.NextRetryAfter
	pick(later.ID)
	soon.Unavailable, soon.Quota.Exceeded = false, false
	soon.Attributes = map[string]string{"priority": "-1"}
	pick(later.ID)
}

func TestSoonestResetFirstSelectorUnknownFallbackAndConcurrent(t *testing.T) {
	now := time.Now()
	s := &SoonestResetFirstSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{{ID: "a"}, {ID: "b"}}
	for _, want := range []string{"a", "b", "a", "b", "a", "b"} {
		got, err := s.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths)
		if err != nil || got.ID != want {
			t.Fatalf("pick = %v, %v; want %s", got, err, want)
		}
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if _, err := s.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, auths); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := s.Pick(context.Background(), "gemini", "model", cliproxyexecutor.Options{}, nil); err == nil {
		t.Fatal("expected no-candidates error")
	}
}

func TestSoonestResetFirstManagerUsesLiveObservations(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	manager := NewManager(nil, &SoonestResetFirstSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "claude"})
	if manager.useSchedulerFastPath() {
		t.Fatal("reset strategy must not use cached round-robin scheduler")
	}
	for _, a := range []*Auth{resetTestAuth("reset-a", now.Add(4*24*time.Hour)), resetTestAuth("reset-z", now.Add(24*time.Hour))} {
		registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "reset-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
		if _, err := manager.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	pick := func(want string) {
		t.Helper()
		got, err := manager.SelectAuth(ctx, "claude", "reset-model", cliproxyexecutor.Options{})
		if err != nil || got.ID != want {
			t.Fatalf("pick = %v, %v; want %s", got, err, want)
		}
	}
	pick("reset-z")
	resultCtx := logging.WithResponseHeadersHolder(ctx)
	logging.SetResponseHeaders(resultCtx, http.Header{
		"Anthropic-Ratelimit-Unified-7d-Reset": []string{strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
	})
	manager.MarkResult(resultCtx, Result{AuthID: "reset-a", Provider: "claude", Model: "reset-model", Success: true})
	pick("reset-a")
}

func TestSoonestResetFirstSelectorKeepsAffinity(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelector(&SoonestResetFirstSelector{})
	t.Cleanup(selector.Stop)
	a := resetTestAuth("a", now.Add(time.Hour))
	b := resetTestAuth("b", now.Add(4*24*time.Hour))
	auths := []*Auth{a, b}
	pick := func(session, want string) {
		t.Helper()
		opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation_id":"` + session + `"}`)}
		got, err := selector.Pick(context.Background(), "claude", "model", opts, auths)
		if err != nil || got.ID != want {
			t.Fatalf("pick = %v, %v; want %s", got, err, want)
		}
	}
	pick("old-session", "a")
	b.Quota = resetTestAuth("", now.Add(30*time.Minute)).Quota
	pick("old-session", "a")
	pick("new-session", "b")
}

func TestSoonestResetFirstSelectorStableTies(t *testing.T) {
	now := time.Now()
	selector := &SoonestResetFirstSelector{}
	auths := []*Auth{resetTestAuth("z", now.Add(time.Hour)), resetTestAuth("a", now.Add(time.Hour))}
	for range 5 {
		got, err := selector.Pick(context.Background(), "claude", "model", cliproxyexecutor.Options{}, auths)
		if err != nil || got.ID != "a" {
			t.Fatalf("pick = %v, %v; want a", got, err)
		}
	}
}
