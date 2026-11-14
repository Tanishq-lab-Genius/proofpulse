#!/usr/bin/env bash
set -Eeuo pipefail

: "${OPENROUTER_API_KEY:?Set OPENROUTER_API_KEY in .env.docker}"
: "${BIFROST_API_KEY:?Set BIFROST_API_KEY in .env.docker}"
: "${TAVILY_API_KEY:?Set TAVILY_API_KEY in .env.docker}"
: "${BIFROST_MODEL:=openrouter/nvidia/nemotron-3.5-super-vl:free}"

export PORT="${PORT:-8000}"
mkdir -p /data/bifrost
sed "s/__PROOFPULSE_PORT__/${PORT}/g" /opt/bifrost/config.template.json > /data/bifrost/config.json

/usr/local/bin/bifrost-http -app-dir /data/bifrost -host 127.0.0.1 -port 8080 &
bifrost_pid=$!
proofpulse_pid=""

cleanup() {
  if [[ -n "${proofpulse_pid}" ]]; then kill "${proofpulse_pid}" 2>/dev/null || true; fi
  kill "${bifrost_pid}" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

ready=0
for _ in $(seq 1 120); do
  if ! kill -0 "${bifrost_pid}" 2>/dev/null; then
    echo "Bifrost exited before becoming healthy" >&2
    exit 1
  fi
  if curl -fsS http://127.0.0.1:8080/health >/dev/null; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "${ready}" != "1" ]]; then
  echo "Bifrost did not become healthy within 120 seconds" >&2
  exit 1
fi

export ADDR="0.0.0.0:${PORT}"
export BIFROST_BASE_URL="http://127.0.0.1:8080/v1"
/usr/local/bin/proofpulse &
proofpulse_pid=$!

set +e
wait -n "${bifrost_pid}" "${proofpulse_pid}"
status=$?
set -e
exit "${status}"
