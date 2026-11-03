package auth

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetFirstSelector spends the available subscription budget whose
// longest quota window resets first. It uses passive observations, never token
// expiry or scheduler retry deadlines, to determine the budget's reset time.
type SoonestResetFirstSelector struct {
	mu       sync.Mutex
	probedAt map[string]time.Time
	fallback RoundRobinSelector
	nowFunc  func() time.Time
}

const resetDiscoveryInterval = 5 * time.Minute

func (s *SoonestResetFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	if s.nowFunc != nil {
		now = s.nowFunc()
	}
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probedAt == nil {
		s.probedAt = make(map[string]time.Time)
	}
	// Bound discovery state even when credentials are repeatedly replaced. Expired
	// entries need not be retained: their next request is eligible for discovery.
	for id, at := range s.probedAt {
		if !now.Before(at.Add(resetDiscoveryInterval)) {
			delete(s.probedAt, id)
		}
	}
	var best, discover *Auth
	var earliest time.Time
	for _, candidate := range available {
		reset := subscriptionResetTime(candidate, now)
		if reset.IsZero() {
			if _, probed := s.probedAt[candidate.ID]; !probed && discover == nil {
				discover = candidate
			}
			continue
		}
		if best == nil || reset.Before(earliest) {
			best, earliest = candidate, reset
		}
	}
	// Learn each account's reset rather than permanently starving unknown
	// accounts as soon as the first account returns a usable observation.
	if discover != nil {
		if len(s.probedAt) >= 4096 {
			clear(s.probedAt)
		}
		s.probedAt[discover.ID] = now
		return discover, nil
	}
	if best != nil {
		return best, nil
	}
	return s.fallback.Pick(ctx, provider, model, opts, available)
}

// subscriptionResetTime deliberately ignores unrelated additional/model-specific
// limits. A stale long-window reset is unknown, not a reason to switch to the
// short window or invent a new reset by adding a fixed duration.
func subscriptionResetTime(auth *Auth, now time.Time) time.Time {
	q := auth.Quota
	var reset time.Time
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		for _, key := range []string{
			"Anthropic-Ratelimit-Unified-7d-Reset",
			"Anthropic-Ratelimit-Unified-5h-Reset",
			"Anthropic-Ratelimit-Unified-Reset",
		} {
			if raw := q.Signals[key]; raw != "" {
				reset = parseSubscriptionReset(raw)
				break
			}
		}
	case "codex":
		var longest int64
		for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
			minutes, errParse := strconv.ParseInt(q.Signals[prefix+"Window-Minutes"], 10, 64)
			if errParse != nil || minutes <= 0 || minutes < longest {
				continue
			}
			candidate := parseSubscriptionReset(q.Signals[prefix+"Reset-At"])
			if candidate.IsZero() && !q.ObservedAt.IsZero() {
				seconds, errSeconds := strconv.ParseInt(q.Signals[prefix+"Reset-After-Seconds"], 10, 64)
				if errSeconds == nil && seconds >= 0 && seconds <= math.MaxInt64/int64(time.Second) {
					candidate = q.ObservedAt.Add(time.Duration(seconds) * time.Second)
				}
			}
			if minutes > longest || reset.IsZero() || (!candidate.IsZero() && candidate.Before(reset)) {
				longest, reset = minutes, candidate
			}
		}
	case "devin":
		for _, key := range []string{"weekly_quota_reset_at", "daily_quota_reset_at"} {
			if raw := q.Signals[key]; raw != "" {
				reset = parseSubscriptionReset(raw)
				break
			}
		}
	}
	if !reset.After(now) {
		return time.Time{}
	}
	return reset
}

func parseSubscriptionReset(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds > 0 {
		return time.Unix(seconds, 0)
	}
	parsed, _ := time.Parse(time.RFC3339Nano, raw)
	return parsed
}
