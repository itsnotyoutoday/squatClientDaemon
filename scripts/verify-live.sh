#!/usr/bin/env bash
# Live smoke test: build squatterd, start it on a temp data dir, pair, open a
# REAL session to a public MUD, check the banner arrives with spans, detach,
# re-attach with `since` (no duplicates), disconnect. Needs network access.
set -euo pipefail
cd "$(dirname "$0")/.."
HOST="${HOST:-startrekkin.net}"; PORT="${PORT:-1701}"; SWP_PORT="${SWP_PORT:-39327}"
TMP="$(mktemp -d)"
PID=""
trap '[ -n "$PID" ] && kill "$PID" 2>/dev/null || true' EXIT
CGO_ENABLED=0 go build -o "$TMP/squatterd" ./cmd/squatterd
"$TMP/squatterd" serve --data-dir "$TMP/data" --port "$SWP_PORT" --bind 127.0.0.1 >"$TMP/daemon.log" 2>&1 &
PID=$!
sleep 1
OUT="$("$TMP/squatterd" pair --data-dir "$TMP/data" --port "$SWP_PORT")"
echo "$OUT"
CODE="$(echo "$OUT" | awk '/Pairing code/ {print $3}')"
FP="$(echo "$OUT" | awk '/TLS fingerprint/ {print $3}')"
go run ./scripts/livecheck --url "wss://127.0.0.1:$SWP_PORT/swp" --code "$CODE" --fp "$FP" --host "$HOST" --port "$PORT"
echo "temp dir (safe to delete): $TMP"
