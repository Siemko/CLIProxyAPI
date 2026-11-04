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
	available = preferUnexhaustedSubscriptionAuths(available, now)
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

// Passive exhaustion is a preference within the already eligible priority tier,
// not a cooldown. Keep exhausted accounts as a last resort when all are exhausted.
// The usual affinity wrapper still takes precedence over this selector.
func preferUnexhaustedSubscriptionAuths(auths []*Auth, now time.Time) []*Auth {
	var preferred []*Auth
	for i, candidate := range auths {
		windows := subscriptionQuotaWindows(candidate)
		exhausted := false
		for _, window := range windows {
			if window.exhaustedAt(candidate.Quota.ObservedAt, now) {
				exhausted = true
				break
			}
		}
		if exhausted {
			if preferred == nil {
				preferred = make([]*Auth, 0, len(auths))
				preferred = append(preferred, auths[:i]...)
			}
		} else if preferred != nil {
			preferred = append(preferred, candidate)
		}
	}
	if len(preferred) > 0 {
		return preferred
	}
	return auths
}

type subscriptionQuotaWindow struct {
	reset     time.Time
	minutes   float64
	present   bool
	exhausted bool
}

func (w subscriptionQuotaWindow) exhaustedAt(observedAt, now time.Time) bool {
	if !w.exhausted {
		return false
	}
	until := w.reset
	if until.IsZero() && !observedAt.IsZero() {
		// Without a stated reset, distrust the exhaustion hint after five minutes.
		// This is a freshness bound, never a synthetic subscription reset.
		until = observedAt.Add(resetDiscoveryInterval)
	}
	return until.After(now)
}

// subscriptionQuotaWindows returns account-wide windows in reset-ranking order.
// Additional/model-specific and code-review quotas must not rank the whole account.
func subscriptionQuotaWindows(auth *Auth) [3]subscriptionQuotaWindow {
	var windows [3]subscriptionQuotaWindow
	q := auth.Quota
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		for i, prefix := range []string{"Anthropic-Ratelimit-Unified-7d-", "Anthropic-Ratelimit-Unified-5h-", "Anthropic-Ratelimit-Unified-"} {
			raw := subscriptionSignal(q, prefix+"Reset")
			used, _ := subscriptionNumber(subscriptionSignal(q, prefix+"Utilization"))
			windows[i] = subscriptionQuotaWindow{
				reset:     parseSubscriptionReset(raw),
				present:   raw != "",
				exhausted: used >= 1 || strings.EqualFold(subscriptionSignal(q, prefix+"Status"), "rejected"),
			}
		}
	case "codex":
		for i, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
			rawReset := subscriptionSignal(q, prefix+"Reset-At")
			rawAfter := subscriptionSignal(q, prefix+"Reset-After-Seconds")
			rawMinutes := subscriptionSignal(q, prefix+"Window-Minutes")
			rawUsed := subscriptionSignal(q, prefix+"Used-Percent")
			reset := parseSubscriptionReset(rawReset)
			if reset.IsZero() && !q.ObservedAt.IsZero() {
				seconds, okSeconds := subscriptionNumber(rawAfter)
				if okSeconds && seconds >= 0 && seconds < float64(math.MaxInt64)/float64(time.Second) {
					reset = q.ObservedAt.Add(time.Duration(seconds * float64(time.Second)))
				}
			}
			minutes, _ := subscriptionNumber(rawMinutes)
			if minutes <= 0 || minutes >= float64(math.MaxInt64)/float64(time.Minute) {
				minutes = 0
			}
			used, _ := subscriptionNumber(rawUsed)
			windows[i] = subscriptionQuotaWindow{
				reset: reset, minutes: minutes, exhausted: used >= 100,
				present: rawReset != "" || rawAfter != "" || rawMinutes != "" || rawUsed != "",
			}
		}
		primary, secondary := windows[0], windows[1]
		if secondary.minutes > primary.minutes ||
			primary.minutes == 0 && secondary.minutes == 0 && secondary.present ||
			primary.minutes > 0 && primary.minutes == secondary.minutes &&
				(primary.reset.IsZero() || !secondary.reset.IsZero() && secondary.reset.Before(primary.reset)) {
			windows[0], windows[1] = secondary, primary
		}
		// A bare limit flag has no window of its own. Use the earliest reported
		// reset, or the freshness bound. A specific exhausted window wins over it.
		if !primary.exhausted && !secondary.exhausted && strings.EqualFold(subscriptionSignal(q, "X-Codex-Limit-Reached"), "true") {
			reset := primary.reset
			if reset.IsZero() || !secondary.reset.IsZero() && secondary.reset.Before(reset) {
				reset = secondary.reset
			}
			windows[2] = subscriptionQuotaWindow{reset: reset, exhausted: true}
		}
	case "devin":
		for i, key := range []string{"weekly_quota_reset_at", "daily_quota_reset_at"} {
			raw := subscriptionSignal(q, key)
			windows[i] = subscriptionQuotaWindow{reset: parseSubscriptionReset(raw), present: raw != ""}
		}
	}
	return windows
}

// A stale or malformed long-window reset is unknown, not a reason to switch to
// the short window or invent the next reset by adding a fixed duration.
func subscriptionResetTime(auth *Auth, now time.Time) time.Time {
	for _, window := range subscriptionQuotaWindows(auth) {
		if window.present {
			if window.reset.After(now) {
				return window.reset
			}
			break
		}
	}
	return time.Time{}
}

func subscriptionSignal(q QuotaState, key string) string {
	if value, ok := q.Signals[key]; ok {
		return strings.TrimSpace(value)
	}
	// Live HTTP observations are canonicalized, but imported snapshots need not be.
	for name, value := range q.Signals {
		if strings.EqualFold(name, key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func subscriptionNumber(raw string) (float64, bool) {
	number, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if errParse != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

func parseSubscriptionReset(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if seconds, okSeconds := subscriptionNumber(raw); okSeconds && seconds > 0 && seconds < 253402300800 {
		// The exclusive upper bound is year 10000, outside RFC 3339's range.
		whole, fraction := math.Modf(seconds)
		return time.Unix(int64(whole), int64(fraction*float64(time.Second)))
	}
	parsed, _ := time.Parse(time.RFC3339Nano, raw)
	return parsed
}
