# Soonest-reset-first routing

Local addition, currently based on CLIProxyAPI v8.0.80 (f5c5681a).
Originally written against v8.0.43 (30780351); re-applied cleanly on v8.0.52
through v8.0.80, including the quota-availability improvements.

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

## Antigravity variants and custom dashboard

Antigravity registration exposes the account-reported low, medium, high and
xhigh variants of known model families, including reported tiered variants.
The exact upstream model ID is retained through normal and streaming execution;
these are not aliases that silently select the highest-effort model. Catalog
metadata is inherited from the known family, while provider eligibility,
credential exclusions, aliases, prefixes and soonest-reset-first routing retain
their existing precedence. Unknown families, unreported variants, unsupported
effort levels and provider-disabled models are not added.

The custom dashboard source is `assets/management-custom.html`; its installed
copy is `~/.cli-proxy-api/static/management.html`. It supports Claude/Fable,
Codex and Antigravity live quota readings through credential-scoped management
API calls. These checks do not issue generation requests. Antigravity quota
meters use Google's per-model remaining fractions and reset timestamps. Newly
added accounts receive one initial check without repeatedly probing on each poll.

Long model names wrap above their meters; hover or keyboard focus reveals the
full display name and model ID. Credential cards are capped at 640px, with an
independently scrollable quota list and visible account controls. Scroll position
survives polling updates.

Run the dashboard regression checks with:

```sh
node --test test/management_dashboard_test.cjs
```

Back up the installed page before copying `assets/management-custom.html` into
the private static directory. The existing `management.disable-auto-update-panel:
true` setting protects this custom page from replacement by the upstream panel
updater. Changes to the page do not require restarting the proxy; hard-refresh
the browser after installing them. Never commit private configuration, credentials
or browser storage with the dashboard source.

## OpenCode Antigravity 429 compatibility

The 2026-11-21 OpenCode investigation reproduced Google's generic HTTP 429
`RESOURCE_EXHAUSTED` response with the actual system instructions and tool
schemas, even with generation capped at 128 tokens. The same tool schemas with
a short prompt succeeded, as did a neutral system instruction of the same length.
Clearing the proxy's retained cooldown alone did not resolve the actual workload.

Changing only `tools.opencode.` to the equivalent JavaScript property access
`tools["opencode"].` in the full system instruction made the native streaming
request succeed. The provider's internal reason for rejecting the dotted form is
not exposed by its generic error response. The installed configuration now uses
the existing, opt-in Antigravity system-instruction replacement facility:

```yaml
oauth:
  providers:
    antigravity:
      sensitive-words-replace:
        'tools.opencode.': 'tools["opencode"].'
```

This changes notation only: exact tool IDs, model IDs, user messages and actual
tool declarations are preserved. It applies only to Antigravity, does not remove
instructions, and does not redirect models, disable cooling or enable paid-credit
fallback. The running service hot-reloaded the rule without replacing the binary
or restarting. This is a local compatibility workaround, not a provider default.

The private configuration was backed up to
`~/.cli-proxy-api/config.yaml.before-antigravity-opencode-namespace-20261121T130416217243Z`.
A parsed comparison verified that this rule is the only configuration change.
The live OpenAI-compatible streaming API then returned HTTP 200, a completed SSE
response, and `OK` with the original 28,358-character system instruction and all
13 tool schemas retained. That live diagnostic substituted only a short user
turn requesting `OK`; the native capped probe retained the original user turn.
No tools were executed. Private prompts, configuration and credentials are not
included in the regression tests or committed source.

The namespace regression covers both system-instruction field spellings, string
and parts representations, unchanged user messages/tool declarations/model IDs,
and idempotence. It passed 10 race-detector runs. The server compile check,
`go vet` for executor helpers and all 10 dashboard checks also passed.

## Local installation

The 2026-11-21 upstream update is deployed as
`8.0.80-soonest-reset-first-improved`, built with Go 1.26.11 from `4cc20e04`
on top of upstream v8.0.80 (`f5c5681a`). The installed and running binary both
have SHA-256
`36e5de01940c18e1051f2a70c370dcaec36c00ab24a83afb0403ad82bcc4121d`.
The previous binary is preserved as
`~/.local/bin/cli-proxy-api-reset-first.before-v8.0.80-20261121T123044671634Z`.
Deployment used atomic replacement with automatic rollback on failed health or
catalog validation. All 68 pre-upgrade models, including all 20 standard
reasoning variants, remain in the authenticated model catalog. The configuration
and installed custom dashboard checksums are unchanged. Previous custom branches
remain available for rollback.

The previous 2026-11-21 Antigravity and dashboard update was deployed as
`8.0.69-soonest-reset-first-improved`, built with Go 1.26.11 from `c9ad9447`.
The installed and running binary both have SHA-256
`18f69d188dee1068769a209b54e89a1b931739118b381ebcece427922ec6bd43`.
The previous binary is preserved as
`~/.local/bin/cli-proxy-api-reset-first.before-antigravity-variants-20261121T121318357007Z`.
The authenticated model catalog now contains 68 models, retaining all 52 models
from immediately before deployment. All 20 account-reported standard reasoning
variants across Gemini and Claude were verified in the live catalog. The private
configuration, credentials and service override were not changed by this update.

The previous 2026-11-13 upstream update was deployed as
`8.0.69-soonest-reset-first-improved`, built with Go 1.26.11 from `21a57dec`
on top of upstream v8.0.69 (`f7ae8fee`). The installed and running binary both
have SHA-256
`3ef1a89c5c9d816a511f639becfaa33046c11da41d4fa4320dbcaa4a2f2fbee7`.
The previous binary is preserved as
`~/.local/bin/cli-proxy-api-reset-first.before-v8.0.69-20261113T081650046976Z`.
Deployment used atomic binary replacement with automatic rollback on a failed
smoke check. The private configuration, credential directory, service override
and package-managed binary are unchanged.

The previous 2026-11-09 upstream update was deployed as
`8.0.61-soonest-reset-first-improved`, built from `e1486f06` on top of upstream
v8.0.61 (`681ce41b`). The installed and running binary both have SHA-256
`2498f06f4631f0ae7ae20bbea01c3a3ddafcc0d43c6e0e27d22d077fd9b45909`.
The previous binary is preserved as
`~/.local/bin/cli-proxy-api-reset-first.before-v8.0.61-20261109T103333493357Z`.
The private configuration, credential directory, service override and packaged
binary are unchanged. Additional affinity-compatibility regression tests accompany
this deployment record.

The previous 2026-11-05 upstream update was deployed as
`8.0.57-soonest-reset-first-improved`, built from `8f96680f`. The systemd user
service runs `~/.local/bin/cli-proxy-api-reset-first` with the existing private
configuration and credential directory, unchanged by that update. At deployment,
the installed and running binary both had SHA-256
`06d5d9853a8362948ed37c18b877a94ff9fdab7063a93cea7270ac165aaf03ce`.
The previous binary is preserved as
`~/.local/bin/cli-proxy-api-reset-first.before-v8.0.57-20261105T211844319532Z`.

The previous 2026-11-04 improvements were deployed as
`8.0.52-soonest-reset-first-improved`. At that deployment the binary had SHA-256
`b8af6e80e288fb8cb0fce919dbdd80848bb93989e00634b751380089111a0728`.
The pre-upgrade binary is preserved as
`~/.local/bin/cli-proxy-api-reset-first.before-quota-improvements-20261104-160635-543514`.

Source: `~/Projects/CLIProxyAPI`, branch `soonest-reset-first-v8.0.80`
(the routing addition and subsequent improvements on top of the upstream tag;
the v8.0.43 version is on branch `soonest-reset-first`).
The previous binary is kept as `~/.local/bin/cli-proxy-api-reset-first.v8.0.43`.
Custom binary: `~/.local/bin/cli-proxy-api-reset-first`.
Service override: `~/.config/systemd/user/cli-proxy-api.service.d/soonest-reset-first.conf`.
The package-managed `/usr/bin/cli-proxy-api` is not replaced. Package upgrades
will not update the custom build; update/rebuild this branch to pick up upstream
changes.

## GitHub fork and synchronization

Fork: [Siemko/CLIProxyAPI](https://github.com/Siemko/CLIProxyAPI).

- `origin`: `git@github.com:Siemko/CLIProxyAPI.git` (personal fork, push destination).
- `upstream`: `https://github.com/router-for-me/CLIProxyAPI.git` (original project).
- Current custom branch: `soonest-reset-first-v8.0.80`.
- Previous custom branch: `soonest-reset-first-v8.0.69`, preserved unchanged,
  tracking its counterpart on `origin`. Older custom branches are also preserved.
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
git cherry-pick v8.0.80..soonest-reset-first-v8.0.80
git push -u origin HEAD
```

Then build:

```sh
go test ./sdk/cliproxy/auth ./sdk/cliproxy ./internal/api/handlers/management
go build -trimpath -ldflags="-s -w -X 'main.Version=X.Y.Z-soonest-reset-first-improved' -X 'main.Commit=<sha>+local-reset-routing'" -o /tmp/opencode/cli-proxy-api-reset-first ./cmd/server
cp -p ~/.local/bin/cli-proxy-api-reset-first ~/.local/bin/cli-proxy-api-reset-first.before-$(date -u +%Y%m%dT%H%M%SZ)
install -m755 /tmp/opencode/cli-proxy-api-reset-first ~/.local/bin/cli-proxy-api-reset-first.new
mv -f ~/.local/bin/cli-proxy-api-reset-first.new ~/.local/bin/cli-proxy-api-reset-first
systemctl --user restart cli-proxy-api
```

To return to the packaged binary, first change `routing.strategy` back to
`round-robin` in `~/.cli-proxy-api/config.yaml`, then remove only the
`soonest-reset-first.conf` override, run `systemctl --user daemon-reload`, and
restart `cli-proxy-api`. Affinity settings and account credentials need no changes.

The pre-activation configuration is backed up at
`~/.cli-proxy-api/config.yaml.before-soonest-reset-first-20261026`.

## Validation

2026-11-21 v8.0.80 upgrade: all custom routing, Antigravity variant and dashboard
commits applied without conflicts. The candidate builds with Go 1.26.11, and all
19 custom Go files are gofmt clean. `go vet` passed for SDK auth, routing/service,
config and runtime executor packages. Focused reset-routing, model discovery,
variant registration and exact normal/streaming request tests passed 10 runs
with the race detector. All 10 dashboard regression checks passed.

The full Go suite passed on rerun. As in the v8.0.69 validation, the first run
hit only `TestServiceHeartbeatFailoverDoesNotDrainBlockingScope`'s one-second
replacement-subscription deadline; the separate service suite then passed, as
did the full rerun. No Home implementation or timing tests were modified locally.
A read-only test using v8.0.80's config parser accepted the actual production
configuration and retained soonest-reset-first, session affinity and custom-panel
auto-update protection without modifying the configuration file.

Upgrade verification confirmed the running executable matches the validated
candidate, the systemd user service is active with zero automatic restarts, and
authenticated `/v1/models` returns HTTP 200 with all 68 pre-upgrade models. Private
configuration and the custom dashboard are byte-for-byte unchanged. No live
generation request was used for this upgrade smoke check.

2026-11-21 Antigravity update: the full Go suite passed on rerun; the initial
run hit `TestServiceHeartbeatFailoverDoesNotDrainBlockingScope`'s one-second
replacement-subscription deadline. The separate service suite passed and no Home
implementation was changed. Focused discovery, variant registration, exact
non-streaming/streaming request construction and soonest-reset-first tests passed
10 runs with the race detector. `go vet` passed for auth and service packages.
The server was built from a clean, committed worktree.

All 10 portable dashboard regression checks passed. Chromium verified 35 actual
provider quota readings, preserved Fable display, readable model names and model-ID
tooltips, keyboard focus, viewport-contained tooltips, the 640px card height cap,
scrollable quota panels, visible controls and scroll-position retention on updates.
The installed page matches the committed asset, and automatic panel replacement
is disabled in the existing private configuration. Service verification confirmed
the correct candidate binary, zero automatic restarts, unchanged configuration
checksum and HTTP 200 from the authenticated model catalog. No live generation
request was used for validation.

2026-11-13 upstream update: all custom commits applied cleanly to v8.0.69.
The full `GOMAXPROCS=4 go test -p 1 ./...` suite passed, as did the separate
auth, service and management package run. Focused subscription-reset, routing,
pinning, alias/cooldown and affinity tests passed 10 runs with `-race`.
`go vet` passed for auth, service, config and management packages. Custom Go
changes are gofmt clean; two unrelated upstream test helpers retain their
upstream formatting rather than introducing formatting-only changes.

Additional regression coverage verifies that forced cooldowns survive disabled
ordinary cooling and configuration reloads, apply across case/thinking-suffix
variants of a model, and leave passive subscription-reset observations intact.
This runs with both bare and affinity-wrapped routing, for model- and
credential-scoped failures. Zero/large weights do not override reset ranking.
Upstream's non-weighted pinning matrix now also covers soonest-reset-first,
including mixed-provider and affinity paths.

Deployment verification: the service is active on `127.0.0.1:8317`, running the
validated clean-source candidate with zero automatic restarts and four credential
entries loaded. Authenticated `/v1/models` returned HTTP 200 with all 38 models
available before the restart. The configuration checksum is unchanged;
soonest-reset-first and one-hour session affinity remain enabled. No generation
request was issued for the smoke check. The prior custom branches remain intact.

2026-11-09 upstream update: both routing commits applied cleanly to v8.0.61.
The server builds with Go 1.26.10, all Go files are gofmt clean, and `go vet`
passes for the auth, service and config packages. Auth, service and management
package suites pass. Focused reset and routing tests pass 10 runs with `-race`,
including new regression coverage for upstream's `session-affinity-rebind-on:
never` mode and all three affinity rebind modes in runtime configuration.

The full `go test -p 1 ./...` run failed only in
`TestResponsesInterruptStopsHTTPUpstream` ("http upstream was not canceled").
Ten focused runs on untouched upstream v8.0.61 reproduced the same failure five
times. No changes were made to that unrelated test or its implementation.

Deployment verification: the service is active on `127.0.0.1:8317`, running the
validated candidate with zero automatic restarts and all four credential entries
loaded. The authenticated `/v1/models` endpoint returned HTTP 200 with all 37
previously available models. The configuration checksum is unchanged;
`soonest-reset-first` and one-hour session affinity remain enabled. The smoke
check did not issue a generation request.

2026-11-05 upstream update: both routing commits applied cleanly to v8.0.57.
The server candidate builds with Go 1.26.10; custom Go files are already gofmt
clean. The full auth and management package suites passed, and the full service
package suite passed on rerun. Focused subscription-reset and soonest-reset-first
tests passed 10 runs with `-race`.

The initial service suite hit
`TestServiceHeartbeatFailoverDoesNotDrainBlockingScope` (replacement subscription
missed its one-second deadline). The focused test passed three custom-build
reruns, while 10 runs on untouched upstream v8.0.57 reproduced the same failure
twice. No changes were made to that unrelated Home timing test or implementation.

The full `go test -p 1 ./...` run failed only in
`TestInPlaceByteWritesAreReviewed` (the new upstream `internal/kvstore/bolt_store.go`
copy is absent from its review allowlist) and
`TestResponsesInterruptStopsHTTPUpstream` (upstream cancellation timing). Both
failures were reproduced on untouched upstream v8.0.57. The service package
passed in the full run. These upstream tests and implementations are unchanged.

Deployment verification: the systemd user service is active on `127.0.0.1:8317`,
running the validated candidate with zero automatic restarts. Four credential
entries loaded; soonest-reset-first and one-hour session affinity remain enabled.
The authenticated `/v1/models` endpoint returned HTTP 200 with 37 models, retaining
all 34 models from immediately before the restart. The configuration checksum is
unchanged. No paid generation was used for this smoke check.

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
