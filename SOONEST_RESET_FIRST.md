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

## Reset data

- Claude: account-wide seven-day reset; five-hour or unified reset only when
  the longer window is not reported.
- Codex: the longer of the base primary and secondary windows, regardless of
  which one is weekly. Absolute reset timestamps take precedence over relative
  seconds; relative seconds are anchored to the observation time, not the time
  of selection. Additional/model-specific and code-review limits are not used
  to rank the account-wide budget.
- Devin: weekly reset, or daily when no weekly reset is reported.

Only actual quota observations are used. OAuth token expiration, Retry-After,
and cooldown backoff are not subscription resets. An expired or malformed reset
is unknown; the selector does not invent the next reset by adding a week.

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

## Local installation

Source: `~/Projects/CLIProxyAPI`, branch `soonest-reset-first-v8.0.52`
(single commit on top of the upstream tag; the v8.0.43 version is on branch `soonest-reset-first`).
The previous binary is kept as `~/.local/bin/cli-proxy-api-reset-first.v8.0.43`.
Custom binary: `~/.local/bin/cli-proxy-api-reset-first`.
Service override: `~/.config/systemd/user/cli-proxy-api.service.d/soonest-reset-first.conf`.
The package-managed `/usr/bin/cli-proxy-api` is not replaced. Package upgrades
will not update the custom build; rebase/rebuild this branch to pick up upstream
changes.

Build with Go 1.26.10 or newer:

To move to a newer upstream tag:

```sh
git fetch origin --tags
git switch -c soonest-reset-first-vX.Y.Z vX.Y.Z
git cherry-pick soonest-reset-first-v8.0.52
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

The server builds, affected-package tests pass, and focused routing/alias/affinity
race tests pass. The full suite has one flaky failure,
`TestResponsesInterruptStopsHTTPUpstream` ("http upstream was not canceled");
the same failure reproduces on untouched v8.0.43. No changes were made to that
unrelated test or its implementation. After activation, the authenticated
`/v1/models` endpoint returned HTTP 200. No paid generation was used for the
live smoke check.
