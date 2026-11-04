# Soonest-reset-first routing

Local addition, currently based on CLIProxyAPI v8.0.52 (6e6afab9).
Originally written against v8.0.43 (30780351); re-applied cleanly on v8.0.52.

```yaml
routing:
  strategy: "soonest-reset-first"
  session-affinity: true
```

For eligible credentials in the highest available priority tier, spend the
account whose subscription quota resets first rather than spreading requests.
Equal reset times use stable credential-ID order. Weights do not affect this
strategy. Disabled credentials, model restrictions, retries and cooldowns keep
their existing behavior.

Within that priority tier, prefer accounts whose passive Claude/Codex headers
do not report an exhausted usage window. This avoids starting a new conversation
on an account whose five-hour budget is already full just because its weekly
reset is earliest. This is a soft preference, not a new cooldown: if every
eligible account reports exhaustion, all remain available as a last resort.
An existing affinity binding is not moved by this preference.

## Reset data

- Claude: account-wide seven-day reset; five-hour or unified reset only when
  the longer window is not reported.
- Codex: the longer of the base primary and secondary windows, regardless of
  which one is weekly. When neither window has a usable length, use the usual
  secondary window, or primary if secondary is absent. Absolute reset timestamps
  take precedence over relative seconds; relative seconds are anchored to the
  observation time, not the time of selection. Additional/model-specific and
  code-review limits are not used to rank the account-wide budget.
- Devin: weekly reset, or daily when no weekly reset is reported.

Only actual quota observations are used. OAuth token expiration, Retry-After,
and cooldown backoff are not subscription resets. An expired or malformed reset
is unknown; the selector does not invent the next reset by adding a week.
Finite fractional timestamps, delays and window lengths are supported. Signal
names are read case-insensitively; malformed, non-finite or overflowing numeric
values are ignored. Absolute timestamps must fit RFC 3339's year range.

Exhaustion is reported by Claude's account-wide 7-day, 5-hour or unified status
(`rejected`) or utilization (at least 1), or Codex's base primary/secondary usage
(at least 100%) or account-wide limit flag. A window's exhaustion hint expires
at its stated reset. With no usable reset, the hint expires five minutes after
its observation; without an observation timestamp it is not trusted. That bound
does not create a subscription reset or a scheduler retry deadline. A bare Codex
limit flag uses the earliest reported reset unless a specific window reports
exhaustion. New observation snapshots replace old ones as before.

The strategy uses existing in-memory observations from upstream responses and
quota events. It makes no additional network calls. Observations are not
persisted across restarts, and management-page usage probes do not necessarily
populate the routing snapshot. Each unknown/expired account gets one discovery
request when selected, then no more discovery preference for five minutes.
After discovery, known resets win. If none are known (including unsupported
providers), routing falls back to round-robin. Discovery can temporarily select
an account other than the earliest known reset; this prevents an unseen account
from being permanently ignored. An account can be discovered only when it is
eligible for a request; affinity can delay discovery until a new session.

With session affinity enabled, this strategy chooses the account for a new
binding; an existing conversation stays bound according to the existing
affinity policy. Disable affinity for strict per-request ordering, at the cost
of potentially losing prompt-cache reuse. Explicit pinned credentials and plugin
schedulers retain their existing precedence.

## Comparison with cliproxy-rs

Reviewed the Rust port's
[`scheduler.rs` at v0.4.1](https://github.com/vayungodara/cliproxy-rs/blob/v0.4.1/crates/cpa-server/src/scheduler.rs)
and its `soonest_reset.rs` integration tests. Adopted the ideas of soft exhaustion
ranking, independent Codex usage/reset/length parsing and finite fractional
numeric handling. This is a local Go implementation using the existing passive
observation and selection infrastructure, not a switch to the Rust proxy.

Deliberately retained stable-ID ties (drain an account rather than rotate equal
resets), the five-minute discovery retry (a failed or headerless probe must not
permanently hide an account), and existing cooldown semantics. The Rust port's
rollover-only discovery and one-hour bounded quota cooldown are not adopted.
Without a usable reset, our soft exhaustion hint is trusted for only five minutes
rather than assuming a full five-hour window.

## Local installation

The 2026-11-04 improvements are deployed as
`8.0.52-soonest-reset-first-improved`. The systemd user service runs
`~/.local/bin/cli-proxy-api-reset-first` with the existing private configuration
and credential directory. The installed and running binary both have SHA-256
`b8af6e80e288fb8cb0fce919dbdd80848bb93989e00634b751380089111a0728`.
The pre-upgrade binary is preserved as
`~/.local/bin/cli-proxy-api-reset-first.before-quota-improvements-20261104-160635-543514`.

Source: `~/Projects/CLIProxyAPI`, branch `soonest-reset-first-v8.0.52`
(the routing addition and subsequent improvements on top of the upstream tag;
the v8.0.43 version is on branch `soonest-reset-first`).
The previous binary is kept as `~/.local/bin/cli-proxy-api-reset-first.v8.0.43`.
Custom binary: `~/.local/bin/cli-proxy-api-reset-first`.
Service override: `~/.config/systemd/user/cli-proxy-api.service.d/soonest-reset-first.conf`.
The package-managed `/usr/bin/cli-proxy-api` is not replaced. Package upgrades
will not update the custom build; rebase/rebuild this branch to pick up upstream
changes.

## GitHub fork and synchronization

Fork: [Siemko/CLIProxyAPI](https://github.com/Siemko/CLIProxyAPI).

- `origin`: `git@github.com:Siemko/CLIProxyAPI.git` (personal fork, push destination).
- `upstream`: `https://github.com/router-for-me/CLIProxyAPI.git` (original project).
- Custom branch: `soonest-reset-first-v8.0.52`, tracking its counterpart on `origin`.
- Keep runtime configuration, client keys and OAuth credentials outside this
  repository. Publish source, tests and this operational guide only.

For future edits on the custom branch, review the diff, run the tests, commit
the intended files and `git push`. A push synchronizes source only; it does not
rebuild or restart the locally installed proxy. No unattended upstream merging
or production deployment is configured.

Build with Go 1.26.10 or newer:

To move to a newer upstream tag:

```sh
git fetch upstream --tags
git switch -c soonest-reset-first-vX.Y.Z vX.Y.Z
git cherry-pick v8.0.52..soonest-reset-first-v8.0.52
git push -u origin HEAD
```

Then build:

```sh
go test ./sdk/cliproxy/auth ./sdk/cliproxy ./internal/api/handlers/management
go build -trimpath -ldflags="-s -w -X 'main.Version=X.Y.Z-soonest-reset-first' -X 'main.Commit=<sha>+local-reset-routing'" -o /tmp/opencode/cli-proxy-api-reset-first ./cmd/server
install -m755 /tmp/opencode/cli-proxy-api-reset-first ~/.local/bin/cli-proxy-api-reset-first
systemctl --user restart cli-proxy-api
```

To return to the packaged binary, first change `routing.strategy` back to
`round-robin` in `~/.cli-proxy-api/config.yaml`, then remove only the
`soonest-reset-first.conf` override, run `systemctl --user daemon-reload`, and
restart `cli-proxy-api`. Affinity settings and account credentials need no changes.

The pre-activation configuration is backed up at
`~/.cli-proxy-api/config.yaml.before-soonest-reset-first-20261026`.

## Validation

2026-11-04 review: the full `sdk/cliproxy/auth`, `sdk/cliproxy` and
`internal/api/handlers/management` package suites passed with `GOMAXPROCS=4`
and `-p 1`. Focused reset/selector tests passed 10 runs with `-race`.
Earlier concurrent runs encountered varying failures in unrelated service
timing/auth-sync tests; both unchanged HEAD (tested with a Go source overlay)
and the patched service package passed standalone. The server candidate builds
with Go 1.26.10. These checks use test fixtures; no live generation was performed.

Deployment verification: the service is active on `127.0.0.1:8317`, all three
credential entries loaded, `soonest-reset-first` and one-hour session affinity
remain enabled, and the authenticated `/v1/models` endpoint returned HTTP 200
with all 33 previously available models. The running executable's checksum
matches the validated candidate. No paid generation was used for this check.

Original installation validation:

The server builds, affected-package tests pass, and focused routing/alias/affinity
race tests pass. The full suite has one flaky failure,
`TestResponsesInterruptStopsHTTPUpstream` ("http upstream was not canceled");
the same failure reproduces on untouched v8.0.43. No changes were made to that
unrelated test or its implementation. After activation, the authenticated
`/v1/models` endpoint returned HTTP 200. No paid generation was used for the
live smoke check.
