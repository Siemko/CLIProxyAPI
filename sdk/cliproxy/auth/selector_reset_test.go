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
		{"claude fractional epoch", "claude", map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": epoch(soon) + ".5"}, now, soon.Add(500 * time.Millisecond)},
		{"claude noncanonical signal", " CLAUDE ", map[string]string{"anthropic-ratelimit-unified-7d-reset": epoch(soon)}, now, soon},
		{"codex weekly primary", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": epoch(later), "X-Codex-Secondary-Window-Minutes": "300", "X-Codex-Secondary-Reset-At": epoch(soon)}, now, later},
		{"codex weekly secondary", "codex", map[string]string{"X-Codex-Secondary-Window-Minutes": "10080", "X-Codex-Secondary-Reset-At": epoch(later), "X-Codex-Primary-Window-Minutes": "300", "X-Codex-Primary-Reset-At": epoch(soon)}, now, later},
		{"codex relative anchored to observation", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "7200"}, now.Add(-time.Hour), soon},
		{"codex relative without observation", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "7200"}, time.Time{}, time.Time{}},
		{"codex expired relative", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "1"}, now.Add(-time.Hour), time.Time{}},
		{"codex overflowing relative", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "9223372036854775807"}, now, time.Time{}},
		{"codex missing lengths prefers secondary", "codex", map[string]string{"X-Codex-Primary-Reset-At": epoch(soon), "X-Codex-Secondary-Reset-At": epoch(later)}, now, later},
		{"codex only primary without length", "codex", map[string]string{"X-Codex-Primary-Reset-At": epoch(soon)}, now, soon},
		{"codex decimal length and relative reset", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080.0", "X-Codex-Primary-Reset-After-Seconds": "3600.5"}, now, soon.Add(500 * time.Millisecond)},
		{"codex stale absolute beats relative", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": epoch(now), "X-Codex-Primary-Reset-After-Seconds": "3600"}, now, time.Time{}},
		{"codex missing longest reset is unknown", "codex", map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Secondary-Window-Minutes": "300", "X-Codex-Secondary-Reset-At": epoch(soon)}, now, time.Time{}},
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
	// A successful response can report that its last token filled the short
	// window. New selections should hand over without waiting for a 429.
	logging.SetResponseHeaders(resultCtx, http.Header{
		"Anthropic-Ratelimit-Unified-7d-Reset":       []string{strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
		"Anthropic-Ratelimit-Unified-5h-Reset":       []string{strconv.FormatInt(now.Add(30*time.Minute).Unix(), 10)},
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"1"},
	})
	manager.MarkResult(resultCtx, Result{AuthID: "reset-a", Provider: "claude", Model: "reset-model", Success: true})
	pick("reset-z")
	logging.SetResponseHeaders(resultCtx, http.Header{
		"Anthropic-Ratelimit-Unified-7d-Reset":  []string{strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
		"Anthropic-Ratelimit-Unified-5h-Status": []string{"allowed"},
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

func resetTestPick(t *testing.T, selector Selector, provider string, auths []*Auth, want string) {
	t.Helper()
	got, err := selector.Pick(context.Background(), provider, "model", cliproxyexecutor.Options{}, auths)
	if err != nil || got == nil || got.ID != want {
		t.Fatalf("pick = %v, %v; want %s", got, err, want)
	}
}

func TestSoonestResetFirstSelectorPrefersUnexhaustedAccounts(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	epoch := func(at time.Time) string { return strconv.FormatInt(at.Unix(), 10) }
	for _, tt := range []struct {
		name, provider string
		signals        map[string]string
	}{
		{"claude five-hour rejected", "claude", map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected", "Anthropic-Ratelimit-Unified-5h-Reset": epoch(now.Add(time.Hour))}},
		{"claude five-hour full", "claude", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1", "Anthropic-Ratelimit-Unified-5h-Reset": epoch(now.Add(time.Hour))}},
		{"claude weekly full", "claude", map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "1.01"}},
		{"claude unified rejected", "claude", map[string]string{"Anthropic-Ratelimit-Unified-Status": "REJECTED", "Anthropic-Ratelimit-Unified-Reset": epoch(now.Add(time.Hour))}},
		{"claude noncanonical status", "claude", map[string]string{"anthropic-ratelimit-unified-5h-status": " rejected ", "anthropic-ratelimit-unified-5h-reset": epoch(now.Add(time.Hour))}},
		{"codex primary full", "codex", map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "3600"}},
		{"codex secondary full", "codex", map[string]string{"X-Codex-Secondary-Used-Percent": "100.0"}},
		{"codex limit flag", "codex", map[string]string{"X-Codex-Limit-Reached": "true"}},
		{"codex no reset", "codex", map[string]string{"X-Codex-Primary-Used-Percent": "100"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selector := &SoonestResetFirstSelector{nowFunc: func() time.Time { return now }}
			full := resetTestAuth("a-soon-full", now.Add(24*time.Hour))
			ready := resetTestAuth("b-later-ready", now.Add(4*24*time.Hour))
			full.Provider, ready.Provider = tt.provider, tt.provider
			if tt.provider == "codex" {
				full.Quota.Signals = map[string]string{"X-Codex-Secondary-Window-Minutes": "10080", "X-Codex-Secondary-Reset-At": epoch(now.Add(24 * time.Hour))}
				ready.Quota.Signals = map[string]string{"X-Codex-Secondary-Window-Minutes": "10080", "X-Codex-Secondary-Reset-At": epoch(now.Add(4 * 24 * time.Hour))}
			}
			full.Quota.ObservedAt = now
			for k, v := range tt.signals {
				full.Quota.Signals[k] = v
			}
			auths := []*Auth{full, ready}
			for range 3 {
				resetTestPick(t, selector, tt.provider, auths, ready.ID)
			}
			if full.Unavailable || full.Quota.Exceeded || !full.NextRetryAfter.IsZero() {
				t.Fatal("passive exhaustion must not create a hard cooldown")
			}
			resetTestPick(t, selector, tt.provider, []*Auth{full}, full.ID)
			ready.Quota = full.Quota.Clone()
			resetTestPick(t, selector, tt.provider, auths, full.ID)
		})
	}
}

func TestSoonestResetFirstSelectorExhaustionExpiresAndFreshSignalsRecover(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, noReset := range []bool{false, true} {
		t.Run(strconv.FormatBool(noReset), func(t *testing.T) {
			readAt := now
			selector := &SoonestResetFirstSelector{nowFunc: func() time.Time { return readAt }}
			full := resetTestAuth("a", now.Add(24*time.Hour))
			ready := resetTestAuth("b", now.Add(4*24*time.Hour))
			full.Quota.ObservedAt = now
			full.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = "1"
			deadline := now.Add(time.Hour)
			if noReset {
				deadline = now.Add(resetDiscoveryInterval)
			} else {
				full.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Reset"] = strconv.FormatInt(deadline.Unix(), 10)
			}
			auths := []*Auth{full, ready}
			resetTestPick(t, selector, "claude", auths, ready.ID)
			readAt = deadline.Add(-time.Nanosecond)
			resetTestPick(t, selector, "claude", auths, ready.ID)
			readAt = deadline
			resetTestPick(t, selector, "claude", auths, full.ID)
			// A newer snapshot is authoritative, even before the old reset.
			readAt = now
			delete(full.Quota.Signals, "Anthropic-Ratelimit-Unified-5h-Utilization")
			full.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"] = "allowed"
			resetTestPick(t, selector, "claude", auths, full.ID)
		})
	}
}

func TestSoonestResetFirstSelectorIgnoresUnrelatedAndInvalidExhaustion(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, signals := range []map[string]string{
		{"Anthropic-Ratelimit-Unified-5h-Utilization": "NaN"},
		{"Anthropic-Ratelimit-Unified-5h-Utilization": "+Inf"},
		{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.99"},
		{"Anthropic-Ratelimit-Unified-5h-Status": "allowed_warning"},
		{"X-Codex-Primary-Used-Percent": "100"},
	} {
		selector := &SoonestResetFirstSelector{nowFunc: func() time.Time { return now }}
		soon := resetTestAuth("a", now.Add(time.Hour))
		soon.Quota.ObservedAt = now
		for k, v := range signals {
			soon.Quota.Signals[k] = v
		}
		resetTestPick(t, selector, "claude", []*Auth{soon, resetTestAuth("b", now.Add(24*time.Hour))}, soon.ID)
	}
}

func TestSoonestResetFirstSelectorExhaustionPreservesPriorityAndAffinity(t *testing.T) {
	now := time.Now()
	base := &SoonestResetFirstSelector{}
	selector := NewSessionAffinitySelector(base)
	t.Cleanup(selector.Stop)
	soon := resetTestAuth("a", now.Add(time.Hour))
	later := resetTestAuth("b", now.Add(24*time.Hour))
	pick := func(session, want string) {
		t.Helper()
		opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation_id":"` + session + `"}`)}
		got, err := selector.Pick(context.Background(), "claude", "model", opts, []*Auth{soon, later})
		if err != nil || got == nil || got.ID != want {
			t.Fatalf("pick = %v, %v; want %s", got, err, want)
		}
	}
	pick("old", soon.ID)
	soon.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"] = "rejected"
	soon.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Reset"] = strconv.FormatInt(now.Add(30*time.Minute).Unix(), 10)
	pick("old", soon.ID)
	pick("new", later.ID)
	soon.Attributes = map[string]string{"priority": "1"}
	pick("higher-priority", soon.ID)
}

func TestParseSubscriptionResetRejectsNonfiniteAndOutOfRange(t *testing.T) {
	for _, raw := range []string{"NaN", "+Inf", "-Inf", "1e300", "-1", "0", "253402300800", "9223372036854775807"} {
		if got := parseSubscriptionReset(raw); !got.IsZero() {
			t.Errorf("parseSubscriptionReset(%q) = %v; want unknown", raw, got)
		}
	}
}

func TestSoonestResetFirstSelectorUnknownFallbackAvoidsExhaustedAccount(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	selector := &SoonestResetFirstSelector{nowFunc: func() time.Time { return now }}
	full := resetTestAuth("z-full", now.Add(time.Hour))
	full.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = "1"
	auths := []*Auth{{ID: "a", Provider: "claude"}, {ID: "b", Provider: "claude"}, full}
	for _, want := range []string{"a", "b", "a", "b", "a", "b"} {
		resetTestPick(t, selector, "claude", auths, want)
	}
}

func TestSoonestResetFirstSelectorConcurrentDiscoveryIsReservedOnce(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	selector := &SoonestResetFirstSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{{ID: "a-unknown", Provider: "claude"}, resetTestAuth("b-known", now.Add(time.Hour))}
	results := make(chan string, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			got, err := selector.Pick(context.Background(), "claude", "model", cliproxyexecutor.Options{}, auths)
			if err != nil || got == nil {
				t.Errorf("pick = %v, %v", got, err)
				return
			}
			results <- got.ID
		})
	}
	wg.Wait()
	close(results)
	counts := map[string]int{}
	for id := range results {
		counts[id]++
	}
	if counts["a-unknown"] != 1 || counts["b-known"] != 31 {
		t.Fatalf("concurrent picks = %v; want exactly one discovery request", counts)
	}
}

func TestSoonestResetFirstSelectorCodexExhaustionBoundaries(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		signals   map[string]string
		observed  time.Time
		readAfter time.Duration
		want      string
	}{
		{"relative zero clears full window", map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "0", "X-Codex-Limit-Reached": "true"}, now, 0, "a"},
		{"short full expires despite flag", map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "60", "X-Codex-Limit-Reached": "true"}, now, time.Minute, "a"},
		{"both windows full waits for later", map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "60", "X-Codex-Secondary-Used-Percent": "100"}, now, time.Minute, "b"},
		{"bare hint without observation is unknown", map[string]string{"X-Codex-Primary-Used-Percent": "100"}, time.Time{}, 0, "a"},
		{"unanchored delay is unknown", map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "60"}, time.Time{}, 0, "a"},
		{"bare hint expires", map[string]string{"X-Codex-Primary-Used-Percent": "100"}, now, resetDiscoveryInterval, "a"},
		{"code-review limit is unrelated", map[string]string{"X-Codex-Code-Review-Primary-Used-Percent": "100"}, now, 0, "a"},
		{"additional limit is unrelated", map[string]string{"X-Codex-Additional-Other-Primary-Used-Percent": "100"}, now, 0, "a"},
		{"wrong provider is unrelated", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1"}, now, 0, "a"},
		{"nonfinite usage ignored", map[string]string{"X-Codex-Primary-Used-Percent": "+Inf"}, now, 0, "a"},
		{"malformed reset hint expires", map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-After-Seconds": "1e300"}, now, resetDiscoveryInterval, "a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selector := &SoonestResetFirstSelector{nowFunc: func() time.Time { return now.Add(tt.readAfter) }}
			auths := []*Auth{
				{ID: "a", Provider: "codex", Quota: QuotaState{ObservedAt: tt.observed, Signals: map[string]string{"X-Codex-Secondary-Window-Minutes": "10080", "X-Codex-Secondary-Reset-After-Seconds": "86400", "X-Codex-Secondary-Reset-At": strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10)}}},
				{ID: "b", Provider: "codex", Quota: QuotaState{Signals: map[string]string{"X-Codex-Secondary-Window-Minutes": "10080", "X-Codex-Secondary-Reset-At": strconv.FormatInt(now.Add(4*24*time.Hour).Unix(), 10)}}},
			}
			for k, v := range tt.signals {
				auths[0].Quota.Signals[k] = v
			}
			resetTestPick(t, selector, "codex", auths, tt.want)
		})
	}
}
