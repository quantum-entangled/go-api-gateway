#!/usr/bin/env bash
#
# Vegeta load test for the gateway.
#
# Ramps from -start to -max req/s in steps, printing a report per step.
# Watch for p99 latency jumps or error rate increases - that's the ceiling.
# When -start equals -max, it behaves as a sustained fixed-rate test.
#
# Usage:
#   ./loadtest/vegeta/run.sh [flags...]
#
# Examples:
#   ./loadtest/vegeta/run.sh                                    # default: 50->500, 10s steps
#   ./loadtest/vegeta/run.sh -max 5000 -step 250 -start 250     # find ceiling at higher rates
#   ./loadtest/vegeta/run.sh -start 2000 -max 2000 -dur 60s     # sustained 2k req/s for 60s
#   ./loadtest/vegeta/run.sh -conns 200 -max 5000               # custom connection pool
#
# Environment:
#   GATEWAY    gateway base URL    (default: http://localhost:8080)
#   KEY_PATH   RSA private key     (default: $PROJECT_ROOT/example.key)
#   VEGETA     vegeta binary path  (default: vegeta)

set -euo pipefail

VEGETA="${VEGETA:-vegeta}"
GATEWAY="${GATEWAY:-http://localhost:8080}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/results"
mkdir -p "$RESULTS_DIR"

PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TOKEN=$(cd "$SCRIPT_DIR/.." && go run ./cmd/gentoken -key "${KEY_PATH:-$PROJECT_ROOT/example.key}")
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

max=500 step=50 step_dur=10s start=50 conns=100
while [[ $# -gt 0 ]]; do
  case "$1" in
    -max)   max="$2"; shift 2 ;;
    -step)  step="$2"; shift 2 ;;
    -dur)   step_dur="$2"; shift 2 ;;
    -start) start="$2"; shift 2 ;;
    -conns) conns="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^$/s/^# \?//p' "$0"; exit 0 ;;
    *) echo "Unknown flag: $1"; exit 1 ;;
  esac
done

result="${RESULTS_DIR}/ramp-${start}_to_${max}-${TIMESTAMP}"
echo "Ramp: ${start} -> ${max} req/s, step +${step}, ${step_dur} per step, ${conns} connections"
echo "Targets: GET /catalog/products, GET /catalog/products/1, GET /orders/orders (authed)"
echo ""

rate=$start
while [[ $rate -le $max ]]; do
  echo "--- ${rate} req/s ---"
  cat <<TARGETS | "$VEGETA" attack -rate="$rate" -duration="$step_dur" -connections="$conns" -name="ramp-${rate}" | \
    tee -a "${result}.bin" | \
    "$VEGETA" report -type=text
GET ${GATEWAY}/catalog/products

GET ${GATEWAY}/catalog/products/1

GET ${GATEWAY}/orders/orders
Authorization: Bearer ${TOKEN}
TARGETS
  echo ""
  rate=$((rate + step))
done

echo "Results: ${result}.bin"
echo "Plot:    $VEGETA plot ${result}.bin > ${result}.html"
