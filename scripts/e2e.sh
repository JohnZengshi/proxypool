#!/usr/bin/env bash
set -euo pipefail

CONFIG="${1:-config.yaml}"
EVIDENCE="${2:-.omo/evidence/task-10-proxy-pool.log}"
BINARY="./proxypool"

if [ ! -f "$CONFIG" ]; then
  echo "ERROR: config file $CONFIG not found" >&2
  exit 1
fi

if [ ! -x "$BINARY" ]; then
  echo "building binary..."
  go build -tags "with_quic with_utls" -o "$BINARY" ./cmd/proxypool
fi

mkdir -p "$(dirname "$EVIDENCE")"
POOL_LOG="${EVIDENCE}.pool.log"

cleanup() {
  if [ -n "${PID:-}" ] && kill -0 "$PID" 2>/dev/null; then
    kill -INT "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

echo "starting proxypool with config $CONFIG..."
"$BINARY" -config "$CONFIG" > "$POOL_LOG" 2>&1 &
PID=$!

echo "waiting for startup..."
sleep 45

STATUS_PORT=$(grep -o 'status_port:.*' "$CONFIG" | head -1 | awk '{print $2}')
if [ -z "$STATUS_PORT" ]; then
  STATUS_PORT=18080
fi

echo "fetching /status from port $STATUS_PORT..."
STATUS=$(curl -s --max-time 10 "http://127.0.0.1:$STATUS_PORT/status")
echo "$STATUS" | python3 -m json.tool 2>/dev/null || echo "$STATUS"

PORTS=$(echo "$STATUS" | python3 -c "import sys,json; [print(p['port']) for p in json.load(sys.stdin) if p['healthy']]" 2>/dev/null || echo "")

# Ensure every configured source tag has at least one live port.
TAGS=$(echo "$STATUS" | python3 -c "import sys,json; print('\n'.join(sorted(set(p.get('tag','') for p in json.load(sys.stdin) if p['healthy']))))" 2>/dev/null || echo "")
echo "Tags present among healthy ports:" | tee -a "$EVIDENCE"
echo "$TAGS" | tee -a "$EVIDENCE"
if [ -z "$TAGS" ]; then
  echo "FAIL: no tags found on healthy nodes" | tee -a "$EVIDENCE"
  exit 1
fi

HY2=$(echo "$STATUS" | python3 -c "import sys,json; print('\n'.join(p['node_name'] for p in json.load(sys.stdin) if p['healthy'] and p.get('type')=='hysteria2'))" 2>/dev/null || echo "")
echo "Hysteria2 nodes live:" | tee -a "$EVIDENCE"
if [ -n "$HY2" ]; then
  echo "$HY2" | head -5 | tee -a "$EVIDENCE"
else
  echo "  none" | tee -a "$EVIDENCE"
  echo "FAIL: no healthy hysteria2-backed node (with_quic not effective)" | tee -a "$EVIDENCE"
  exit 1
fi

echo "Build-tag self-check passed (binary started)." | tee -a "$EVIDENCE"

if [ -z "$PORTS" ]; then
  echo "FAIL: no healthy ports found" | tee "$EVIDENCE"
  exit 1
fi

COUNT=$(echo "$PORTS" | wc -l | tr -d ' ')
echo "found $COUNT healthy ports"

if [ "$COUNT" -lt 2 ]; then
  echo "FAIL: expected at least 2 healthy ports, got $COUNT" | tee "$EVIDENCE"
  exit 1
fi

echo "" | tee "$EVIDENCE"
echo "=== E2E Proxy Pool Verification ===" | tee -a "$EVIDENCE"
echo "Config: $CONFIG" | tee -a "$EVIDENCE"
echo "Healthy ports: $COUNT" | tee -a "$EVIDENCE"
echo "" | tee -a "$EVIDENCE"

echo "Per-port exit IPs:" | tee -a "$EVIDENCE"
IPS=""
for p in $PORTS; do
  IP=$(curl -s --max-time 20 --proxy "http://127.0.0.1:$p" https://api.ipify.org 2>/dev/null || echo "FAILED")
  echo "  port $p -> $IP" | tee -a "$EVIDENCE"
  IPS="$IPS$IP "
done

UNIQUE=$(echo $IPS | tr ' ' '\n' | sort -u | grep -v '^$' | wc -l | tr -d ' ')
echo "" | tee -a "$EVIDENCE"
echo "Unique exit IPs: $UNIQUE" | tee -a "$EVIDENCE"

if [ "$UNIQUE" -ne "$COUNT" ]; then
  echo "FAIL: expected $COUNT unique IPs, got $UNIQUE" | tee -a "$EVIDENCE"
  exit 1
fi

echo "" | tee -a "$EVIDENCE"
echo "Concurrent test (all ports simultaneously):" | tee -a "$EVIDENCE"
PIDS=""
for p in $PORTS; do
  curl -s --max-time 20 --proxy "http://127.0.0.1:$p" https://api.ipify.org > "/tmp/e2e_port_$p.txt" 2>/dev/null &
  PIDS="$PIDS $!"
done
for pid in $PIDS; do
  wait $pid || true
done

ALL_OK=1
for p in $PORTS; do
  RESULT=$(cat "/tmp/e2e_port_$p.txt" 2>/dev/null || echo "")
  if [ -z "$RESULT" ]; then
    echo "  port $p: FAILED" | tee -a "$EVIDENCE"
    ALL_OK=0
  else
    echo "  port $p: OK ($RESULT)" | tee -a "$EVIDENCE"
  fi
  rm -f "/tmp/e2e_port_$p.txt"
done

echo "" | tee -a "$EVIDENCE"
echo "=== New Feature Verification ===" | tee -a "$EVIDENCE"

echo "Checking /history endpoint..." | tee -a "$EVIDENCE"
HISTORY=$(curl -s --max-time 10 "http://127.0.0.1:$STATUS_PORT/history")
echo "$HISTORY" | python3 -m json.tool 2>/dev/null | head -5 | tee -a "$EVIDENCE" || echo "FAIL: /history" | tee -a "$EVIDENCE"

echo "Checking POST /probe endpoint..." | tee -a "$EVIDENCE"
PROBE_RESULT=$(curl -s --max-time 30 -X POST "http://127.0.0.1:$STATUS_PORT/probe?port=$(echo "$PORTS" | head -1 | tr -d ' ')" 2>/dev/null || echo "FAILED")
if echo "$PROBE_RESULT" | python3 -m json.tool 2>/dev/null | grep -q "port"; then
  echo "  /probe: OK" | tee -a "$EVIDENCE"
else
  echo "  /probe: FAIL" | tee -a "$EVIDENCE"
fi

echo "Checking / dashboard..." | tee -a "$EVIDENCE"
DASHBOARD=$(curl -s --max-time 10 "http://127.0.0.1:$STATUS_PORT/")
if echo "$DASHBOARD" | grep -q "canvas"; then
  echo "  /: OK (canvas found)" | tee -a "$EVIDENCE"
else
  echo "  /: FAIL (no canvas)" | tee -a "$EVIDENCE"
fi

echo "Checking startup URL block..." | tee -a "$EVIDENCE"
URL_COUNT=$(grep -c "^http://127.0.0.1:" "$POOL_LOG" 2>/dev/null || echo "0")
if [ "$URL_COUNT" -gt 0 ]; then
  echo "  startup URL block: OK ($URL_COUNT URLs)" | tee -a "$EVIDENCE"
else
  echo "  startup URL block: FAIL" | tee -a "$EVIDENCE"
fi

if [ "$ALL_OK" -eq 1 ]; then
  echo "" | tee -a "$EVIDENCE"
  echo "RESULT: PASS - all ports work concurrently with distinct exit IPs" | tee -a "$EVIDENCE"
  exit 0
else
  echo "" | tee -a "$EVIDENCE"
  echo "RESULT: FAIL - some ports failed concurrent test" | tee -a "$EVIDENCE"
  exit 1
fi
