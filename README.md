# wrapper-lite-home

**English** · [中文](README.zh-CN.md)

A lightweight API aggregation gateway for Apple Music decryption wrappers.
Routes requests to the correct regional upstream API by detecting the storefront
region of each `adamId` via iTunes lookup, with an AdGuard‑Home‑style admin
dashboard.

It aggregates the upstream **lite** branch of
[WorldObservationLog/wrapper (lite)](https://github.com/WorldObservationLog/wrapper/tree/lite)
— the underlying single-port Apple Music decryption wrapper — and exposes
their endpoints on one unified port. It is designed specifically for the
`lite` branch, so make sure each upstream points to a `lite`-branch instance.

## Features

- **Single‑port aggregation** — expose `/m3u8`, `/key`, `/lyrics`,
  `/webplayback`, `/license` and `/status` on one port while proxying to
  multiple upstream backends.
- **Region‑aware routing** — for each incoming `adamId`, queries the iTunes
  lookup API for every region the upstreams support and forwards the request
  to the upstream that serves the matching region.
- **Detection cache** — iTunes lookup results are cached per region (TTL
  configurable, default 30 min) to avoid repeated lookups.
- **Health probing** — every minute each upstream's `/status` is polled.
  If it fails 3 consecutive times the upstream is marked offline and probed
  only every 10 minutes (backoff). Empty regions are also treated as offline.
- **Admin dashboard** — login‑protected web UI showing daily request
  statistics, per‑API breakdowns, and uptime‑Kuma‑style status cards.
- **Statistics** — total / per‑upstream / per‑endpoint counts, hourly
  breakdown, and 7‑day history, persisted to a JSON file.
- **Zero external dependencies** — built entirely on the Go standard library.

## Configuration

Copy `config.example.json` to `config.json` and adjust:

```json
{
  "listen": ":8080",
  "auth": {
    "username": "admin",
    "password": "admin"
  },
  "session_ttl": "24h",
  "region": {
    "cache_ttl": "30m",
    "not_found_ttl": "10m",
    "concurrency": 4,
    "lookup_timeout": "5s",
    "itunes_lookup_base": "https://itunes.apple.com/lookup"
  },
  "probe": {
    "interval": "1m",
    "retries": 3,
    "retry_delay": "2s",
    "backoff_interval": "10m",
    "timeout": "5s"
  },
  "upstream_timeout": "30s",
  "stats_file": "data/stats.json",
  "stats_save_interval": "30s",
  "upstreams": [
    {
      "name": "US API",
      "base_url": "http://127.0.0.1:3001"
    },
    {
      "name": "CN API",
      "base_url": "http://127.0.0.1:3002"
    }
  ]
}
```

### Fields

| Field | Default | Description |
|-------|---------|-------------|
| `listen` | `:8080` | Listen address |
| `auth.username` | `admin` | Admin login username |
| `auth.password` | `admin` | Admin login password (change on first run!) |
| `session_ttl` | `24h` | Session token lifetime |
| `region.cache_ttl` | `30m` | How long to cache a "found" region for an adamId |
| `region.not_found_ttl` | `10m` | How long to cache a "not found" region |
| `region.concurrency` | `4` | Max parallel iTunes lookups per adamId |
| `region.lookup_timeout` | `5s` | Timeout for each iTunes lookup request |
| `region.itunes_lookup_base` | `https://itunes.apple.com/lookup` | iTunes lookup base URL (use a mirror/proxy in regions where apple.com is blocked) |
| `probe.interval` | `1m` | How often to probe upstream /status in normal mode |
| `probe.retries` | `3` | Number of retries after a failed probe before going into backoff |
| `probe.retry_delay` | `2s` | Delay between retry attempts |
| `probe.backoff_interval` | `10m` | Probe interval in backoff mode |
| `probe.timeout` | `5s` | Timeout for each probe request |
| `upstream_timeout` | `30s` | Timeout for proxied upstream requests |
| `stats_file` | `data/stats.json` | Path to the statistics persistence file |
| `stats_save_interval` | `30s` | How often to flush stats to disk |
| `upstreams[].name` | — | A human‑readable name shown in the dashboard |
| `upstreams[].base_url` | — | Base URL of the upstream wrapper API |
| `upstreams[].enabled` | `true` | Whether this upstream is active |

Duration values accept Go duration strings (`"30s"`, `"5m"`, `"1h"`) or a plain
number of seconds.

## HTTP API

All responses use the following envelope:

```json
{"code":0,"msg":"SUCCESS","data":{...}}
```

### Public endpoints

| Endpoint | Method | Parameters | Description |
|----------|--------|------------|-------------|
| `/m3u8` | GET | `adamId` | Get M3U8 playlist |
| `/key` | GET | `adamId`, optional `uri` | Get decryption key |
| `/lyrics` | GET | `adamId`, optional `language`, optional `syllable` (`1`=syllable-lyrics default, `0`=lyrics) | Get lyrics |
| `/webplayback` | GET | `adamId` | Get web playback data |
| `/license` | POST | JSON: `adamId`, `challenge`, `uri` | Get license |
| `/status` | GET | — | Returns merged `regions` from all online upstreams |

### Admin API

All admin endpoints require authentication. Provide the session cookie
`wl_token` (set after login) or an `Authorization: Bearer <token>` header.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/login` | POST | `{"username":"...","password":"..."}` → sets cookie |
| `/api/logout` | POST | Clears the session |
| `/api/me` | GET | Returns current username |
| `/api/status` | GET | Upstream snapshots (online, regions, latency, uptime, backoff) |
| `/api/stats` | GET | Request statistics (total, today hourly, 7‑day, per‑upstream, per‑endpoint) |

## Admin Dashboard

Open `http://<host>:<port>/` in a browser. The dashboard shows:

- **Stats cards** — total requests, today's count, online upstreams, merged regions
- **Uptime cards** — one per upstream, with online/offline/backoff indicators,
  region badges, latency, uptime percentage, and last check time
- **Hourly chart** — today's requests broken down by hour
- **7‑day chart** — daily totals for the last 7 days
- **Per‑API breakdown** — horizontal bar chart of requests per upstream
- **Per‑endpoint breakdown** — today's requests by endpoint (/m3u8, /key, etc.)

## Region routing logic

1. An incoming request carries an `adamId`.
2. The service fetches the merged list of regions from all *online* upstreams.
3. For each region, the iTunes lookup API is queried:
   `https://itunes.apple.com/lookup?id=<adamId>&country=<region>`
4. If `resultCount` > 0, the adamId is available in that region.
5. The result is cached per‑region (TTL configurable).
6. The request is forwarded to the first online upstream that supports one of
   the available regions (round‑robin per region for load balancing).

If no region can be determined or no upstream is online, the endpoint returns
an appropriate error code (502, 503, 404).

## Quick start

```bash
# 1. Build
go build -o wrapper-lite.exe .

# 2. Create config
cp config.example.json config.json
# edit config.json to set your upstreams and credentials

# 3. Run
./wrapper-lite.exe --config config.json

# 4. Open http://localhost:8080 and log in
```

## Build

```bash
go build -o wrapper-lite.exe .
```

The binary is self‑contained — the frontend is embedded via `go:embed`.

## Test

```bash
go test ./...
```

## Mock servers for offline testing (optional)

The real deployment uses the real iTunes lookup API and your real upstreams —
no mocks needed. The mocks below are only for offline verification in
environments without internet access:

```bash
# terminal 1 — US upstream
go run ./testdata/mock_upstream --name "US" --addr :3001 --regions us

# terminal 2 — CN upstream
go run ./testdata/mock_upstream --name "CN" --addr :3002 --regions cn

# terminal 3 — (offline only) mock iTunes lookup, even adamId -> us, odd -> cn
go run ./testdata/mock_itunes --addr :4000

# start wrapper-lite with a test config, then test
curl http://localhost:8080/status
curl -c cookies.txt -d "{\"username\":\"admin\",\"password\":\"admin\"}" http://localhost:8080/api/login
curl -b cookies.txt "http://localhost:8080/m3u8?adamId=111111111"  # even -> US upstream
```

## License

[MIT](LICENSE)






