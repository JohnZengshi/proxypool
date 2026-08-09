# proxypool

Go proxy pool service that fetches a Clash subscription, probes each node's real exit IP, dedupes by exit IP, and exposes one no-auth HTTP+SOCKS5 port per surviving node.

## What it does

1. Fetches your Clash subscription URL and parses vmess nodes.
2. Connects through each node and probes its public exit IP.
3. Drops dead nodes and dedupes by exit IP (keeps lowest latency per IP).
4. Opens one local port per surviving unique node. Each port supports both HTTP CONNECT and SOCKS5, protocol auto-detected, no authentication.
5. Background health checks (default 20s) and subscription refresh (default 6h).
6. `/status` JSON endpoint lists all ports, exit IPs, latency, and health.
7. Port mapping is persisted to `state.json` so the same exit IP gets the same port across restarts.
8. Every proxied connection is logged to stdout with port, protocol, target, duration, and byte counts.
9. Live monitoring dashboard at `http://127.0.0.1:18080/` with latency sparklines and on-demand probing.

## Quick start

```bash
cp config.example.yaml config.yaml
# edit config.yaml, set your sources (or subscription_url)
make build
./proxypool -config config.yaml
```

Output on startup:

```
PORT  EXIT_IP           LATENCY(ms)  HEALTHY  NODE
TAG      NODE
18081 13.193.127.229    730          yes      日本2
18082 13.196.174.30     698          yes      日本7
...
```

## Using the proxy pool

Each port is a standalone proxy. Put them in your software's proxy list:

```
http://127.0.0.1:18081
http://127.0.0.1:18082
http://127.0.0.1:18083
...
```

Or as SOCKS5:

```
socks5://127.0.0.1:18081
socks5://127.0.0.1:18082
```

All ports can be used simultaneously. No username or password needed.

Test a port:

```bash
curl --proxy http://127.0.0.1:18081 https://api.ipify.org
```

## Status API

- `GET /status` - JSON array of all proxy ports with exit IP, latency, health, source tag, and node name.
- `GET /healthz` - Returns 200 if at least one node is healthy, 503 otherwise.
- `GET /history` - JSON map of port to latency history samples (last hour, 20s intervals).
- `POST /probe` - Trigger an on-demand probe. Optional `?port=N` for a single port; omit for all.
- `GET /` - Live monitoring dashboard with latency sparklines and probe buttons.

```bash
curl http://127.0.0.1:18080/status | python3 -m json.tool
curl http://127.0.0.1:18080/history | python3 -m json.tool
curl -X POST http://127.0.0.1:18080/probe?port=18081
```

## Configuration

| Field | Default | Description |
|---|---|---|
| `sources` | `[]` | List of `{tag,type,url}` sources; type is `clash` or `singbox` |
| `subscription_url` | _(optional)_ | Legacy single Clash URL; promoted to one `default` source when `sources` is empty |
| `bind` | `127.0.0.1` | Bind address for proxy ports |
| `base_port` | `18081` | First port for proxy nodes |
| `status_port` | `18080` | Port for /status and /healthz |
| `state_file` | `./state.json` | Port-to-IP mapping persistence |
| `probe_urls` | `[api.ipify.org, ifconfig.me/ip]` | IP echo services for exit probing |
| `probe_timeout` | `10s` | Timeout per probe attempt |
| `health_interval` | `20s` | Health check interval |
| `refresh_interval` | `6h` | Subscription refresh interval |
| `dial_timeout` | `10s` | TCP dial timeout to proxy server |
| `max_concurrent_probe` | `8` | Max concurrent node probes |
| `log_requests` | `true` | Log every proxied connection to stdout |
| `log_format` | `text` | Log format: `text` or `json` |

CLI flags can override config: `-sub`, `-bind`, `-base-port`.

## Monitoring dashboard

Open `http://127.0.0.1:18080/` in your browser to see a live table of all proxies with:

- Port, proxy URL, exit IP, source tag, node name, health status
- Tag filter (segmented control) to scope the table to one source
- Current latency with color coding (green < 300ms, yellow < 800ms, red > 800ms)
- Latency sparkline charts from the last hour of health checks
- Per-row "probe" button for on-demand latency testing
- "Probe All" button to re-test every proxy at once

The dashboard polls `/status` and `/history` every 5 seconds. Health checks run every 20 seconds.

## Security

This proxy has **no authentication**. It defaults to binding on `127.0.0.1` only. Do not change `bind` to `0.0.0.0` unless you understand that anyone on your network can use your subscription's bandwidth and quota through these ports.

## E2E verification

```bash
bash scripts/e2e.sh config.yaml
```

Verifies all ports return distinct exit IPs and work concurrently.
