# flakybin

[![Go Report Card](https://goreportcard.com/badge/github.com/mikluko/flakybin)](https://goreportcard.com/report/github.com/mikluko/flakybin)
[![GitHub License](https://img.shields.io/github/license/mikluko/flakybin)](https://github.com/mikluko/flakybin/blob/main/LICENSE)
[![GitHub Release](https://img.shields.io/github/v/tag/mikluko/flakybin?label=release)](https://github.com/mikluko/flakybin/tags)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mikluko/flakybin)](https://github.com/mikluko/flakybin/blob/main/go.mod)
[![GitHub Stars](https://img.shields.io/github/stars/mikluko/flakybin)](https://github.com/mikluko/flakybin/stargazers)

Deterministic HTTP chaos for testing uptime monitors and retry logic.

flakybin is a fault-injection server that fails on a deterministic, seed-reproducible
schedule: it returns errors, hangs, and drops connections during scheduled outage windows,
emulating a service that goes down on a repeating cycle.

Outage placement is computed purely from the request URL. The same parameters always yield
the same outage windows on the wall clock, across restarts and across machines. No state,
no database.

## Run

```sh
go run .            # listens on :8080 (override with -addr or PORT)
PORT=9000 go run .

go install github.com/mikluko/flakybin@latest   # or install the binary
```

Then open <http://localhost:8080/> for the interactive schedule explorer, or
<http://localhost:8080/docs> for the full reference.

## Modes

The first path segment selects how outages are placed within each period.

- **jitter** (default) — `count` outages of length `duration` are dropped at seed-hashed
  offsets anywhere within the period. No divisibility constraint; looks like organic flapping.
- **noise** — the period is divided into `N = period/duration` fixed slots; `count` of
  them are outages. `period` must divide evenly by `duration`. Grid-aligned.

## Common parameters

| param | meaning |
| --- | --- |
| `period` | length of one repeating cycle, e.g. `24h`, `1h`, `10m` (default `24h`) |
| `duration` | length of a single outage, e.g. `5m` (default `15m`) |
| `count` | number of outages per period — supply this **or** `uptime` (not both) |
| `uptime` | target availability percent, e.g. `99.9`; converted to a count. Defaults to `90` when neither `count` nor `uptime` is given. `100` → 400 |
| `seed` | RNG seed (default `0`); change to reshuffle placement |

## Endpoints

| route | during an outage |
| --- | --- |
| `GET /{mode}/status/{code}` | responds with HTTP `{code}`; 200 when up. Optional `retry-after=auto` or `retry-after=<seconds>` sets `Retry-After`. |
| `GET /{mode}/hang` | withholds the response until the outage ends, or for `for=<duration>`. Emulates a timeout. |
| `GET /{mode}/drop` | writes `after=<bytes>` of body (default 0) then resets the TCP connection. |
| `GET /{mode}/inspect` | shows the schedule and its outage windows. Browsers get a graphical timeline, scripts get JSON (override with `?format=html`/`?format=json`). Triggers no failure. |

## Examples

```sh
# Jitter (default): ~99% availability over a 24h period, 503 + automatic Retry-After.
curl -i 'localhost:8080/jitter/status/503?period=24h&duration=5m&uptime=99&retry-after=auto'

# Ten 1-minute blips scattered through each hour, seed 42.
curl -i 'localhost:8080/jitter/hang?period=1h&duration=1m&count=10&seed=42'

# Noise: 24h period, 5m slots, 12 down (~99.58% uptime).
curl -i 'localhost:8080/noise/status/503?period=24h&duration=5m&count=12&retry-after=auto'

# Noise, first 10 minutes of every hour are down.
curl -i 'localhost:8080/noise/status/500?period=1h&duration=10m&count=1'

# Inspect the schedule without triggering it.
curl -s 'localhost:8080/jitter/inspect?period=24h&duration=5m&uptime=99'
```

## Response headers

Every response advertises the resolved schedule and current state via `X-Flaky-*` headers:
`Mode`, `Period`, `Duration`, `Count`, `Seed`, `Uptime`, `In-Outage`, and either
`Outage-Ends` or `Next-Outage` (RFC 3339).

## Release

The version comes from the git tag. Tag and push, then build the OCI image (via
[ko](https://ko.build), no Dockerfile) and the Helm chart together:

```sh
git tag v0.1.0 && git push --tags
make build          # ko build --bare + helm package/push to the OCI registry
```

`make build` pushes `ghcr.io/mikluko/flakybin:<version>` and the chart to
`oci://ghcr.io/mikluko/flakybin/charts/flakybin`. Run `make help` for individual targets.

## Deploy

Install from the OCI chart (or the local `charts/flakybin` path):

```sh
helm install flakybin oci://ghcr.io/mikluko/flakybin/charts/flakybin --version 0.1.0 \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=flakybin.example.com
```

The container listens on `:8080`, runs as non-root with a read-only root filesystem,
and uses `GET /` for liveness/readiness. Set `flakybin.quiet=true` to disable
per-request access logging. See `charts/flakybin/values.yaml` for all options.
