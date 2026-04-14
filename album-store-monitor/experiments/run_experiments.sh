#!/bin/bash
# run_experiments.sh — Execute all load test experiments and save CSV data.
# Mirrors the original final_project experiment structure but adapted for the album store.

set -e
ALB="${ALB_URL:-http://album-store-alb-1900454484.us-west-2.elb.amazonaws.com}"
LOADTEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../loadtest" && pwd)"
DATA_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/data" && pwd)"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

run() {
  local name="$1" users="$2" rate="$3" duration="$4" file="${5:-locustfile.py}"
  log "=== $name: $users users, spawn=$rate/s, duration=$duration ==="
  locust -f "$LOADTEST_DIR/$file" \
    --host "$ALB" \
    --users "$users" --spawn-rate "$rate" \
    --run-time "$duration" \
    --headless \
    --csv "$DATA_DIR/$name" \
    --csv-full-history \
    2>&1 | grep -E "^\[|Type|Aggregated|POST|GET|PUT|---"
  log "=== $name complete ==="
  sleep 15  # cool-down between experiments
}

log "Starting Album Store Load Test Experiments"
log "Target: $ALB"
log "Data output: $DATA_DIR"
echo ""

# ── Exp 1: Baseline (light load — measure system at rest) ─────────────────────
run "exp1_baseline" 5 2 "2m"

# ── Exp 2: S11 — Concurrent Album Creates ─────────────────────────────────────
run "exp2_concurrent_creates" 50 10 "3m" "chaosarena_style.py"

# ── Exp 3: S12 — Photo Upload Pipeline (POST→completed) ─────────────────────
run "exp3_photo_uploads" 30 5 "3m" "chaosarena_style.py"

# ── Exp 4: S13 — Mixed Read/Write (cache hit rate study) ────────────────────
run "exp4_mixed_readwrite" 40 8 "3m"

# ── Exp 5: Ramp load (gradual increase) ─────────────────────────────────────
log "=== Exp 5: Ramp (5→60 users over 3 min) ==="
locust -f "$LOADTEST_DIR/locustfile.py" \
  --host "$ALB" \
  --users 60 --spawn-rate 3 \
  --run-time "3m" \
  --headless \
  --csv "$DATA_DIR/exp5_ramp" \
  --csv-full-history \
  2>&1 | grep -E "^\[|Aggregated|---"
sleep 15

# ── Exp 6: Spike (sudden burst) ─────────────────────────────────────────────
log "=== Exp 6: Spike (0→80 users instantly) ==="
locust -f "$LOADTEST_DIR/locustfile.py" \
  --host "$ALB" \
  --users 80 --spawn-rate 80 \
  --run-time "2m" \
  --headless \
  --csv "$DATA_DIR/exp6_spike" \
  --csv-full-history \
  2>&1 | grep -E "^\[|Aggregated|---"
sleep 15

# ── Exp 7: S15 — Large Payload Throughput ─────────────────────────────────
run "exp7_large_payload" 20 5 "3m" "chaosarena_style.py"

log "All experiments complete. Data in: $DATA_DIR"
ls -lh "$DATA_DIR"/*.csv 2>/dev/null | awk '{print $5, $9}'
