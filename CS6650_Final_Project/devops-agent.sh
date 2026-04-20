#!/bin/bash
# devops-agent.sh — Gather album store metrics for Claude Code DevOps analysis.
#
# Usage: paste this script's output into a Claude Code session and ask:
#   "Analyze these metrics and recommend whether to scale up, scale down, or no action."
#
# Claude Code IS the AI agent — no API key, no separate container.
# It reads live data from Prometheus + Kafka analytics + alert logs.

PROMETHEUS="${PROMETHEUS_URL:-http://52.13.41.234:9090}"
MONITOR_ID="${MONITOR_INSTANCE_ID:-i-0a2026bd6f5807820}"

echo "╔══════════════════════════════════════════════════════════════════╗"
echo "║     Album Store — Claude Code DevOps Agent Report               ║"
echo "║     $(date -u '+%Y-%m-%d %H:%M:%S UTC')                              ║"
echo "╚══════════════════════════════════════════════════════════════════╝"
echo ""

# ── Prometheus helper ──────────────────────────────────────────────────────────
prom_query() {
  local query="$1"
  local label="${2:-value}"
  local result
  result=$(curl -s "${PROMETHEUS}/api/v1/query" \
    --data-urlencode "query=${query}" 2>/dev/null \
    | python3 -c "
import sys,json
d=json.load(sys.stdin)
r=d['data']['result']
if r:
    vals=[f\"{x['metric'].get('instance','')}: {float(x['value'][1]):.2f}\" for x in r]
    print(', '.join(vals))
else:
    print('no data')
" 2>/dev/null)
  echo "  ${label}: ${result:-no data}"
}

prom_scalar() {
  local query="$1"
  curl -s "${PROMETHEUS}/api/v1/query" \
    --data-urlencode "query=${query}" 2>/dev/null \
    | python3 -c "
import sys,json
d=json.load(sys.stdin)
r=d['data']['result']
if r: print(f'{float(r[0][\"value\"][1]):.2f}')
else: print('N/A')
" 2>/dev/null
}

# ── Infrastructure State ───────────────────────────────────────────────────────
echo "=== INFRASTRUCTURE ==="
NODES=$(prom_scalar "count(up{job='album_store'}==1)")
echo "  Active c5n.large nodes:  ${NODES}"
echo "  ALB URL:                 http://album-store-alb-1900454484.us-west-2.elb.amazonaws.com"
echo ""

# ── HTTP Performance (last 5 min) ─────────────────────────────────────────────
echo "=== HTTP PERFORMANCE (last 5 min) ==="
echo "  Request rate (req/s):    $(prom_scalar 'rate(aws_applicationelb_request_count_sum[5m])')"
echo "  P95 latency (ms):        $(prom_scalar 'aws_applicationelb_target_response_time_p95 * 1000')"
echo "  P99 latency (ms):        $(prom_scalar 'aws_applicationelb_target_response_time_p99 * 1000')"
echo "  5xx error rate (%):      $(prom_scalar '100 * rate(aws_applicationelb_httpcode_target_5_xx_count_sum[5m]) / (rate(aws_applicationelb_request_count_sum[5m]) + 0.001)')"
echo "  Healthy nodes:           $(prom_scalar 'min(aws_applicationelb_healthy_host_count_average)')"
echo ""

# ── Redis Cache ────────────────────────────────────────────────────────────────
echo "=== REDIS CACHE ==="
HITS=$(prom_scalar "sum(rate(album_store_cache_hits_total[5m]))")
MISSES=$(prom_scalar "sum(rate(album_store_cache_misses_total[5m]))")
HIT_RATE=$(prom_scalar "100 * sum(rate(album_store_cache_hits_total[5m])) / (sum(rate(album_store_cache_hits_total[5m])) + sum(rate(album_store_cache_misses_total[5m])) + 0.001)")
echo "  Cache hit rate (%):      ${HIT_RATE}"
echo "  Hits/s:                  ${HITS}"
echo "  Misses/s (→ DB):         ${MISSES}"
echo "  Seq INCRs/s (uploads):   $(prom_scalar 'sum(rate(album_store_seq_incr_total[5m]))')"
echo "  Redis memory (bytes):    $(prom_scalar 'redis_memory_used_bytes')"
echo "  Redis connected clients: $(prom_scalar 'redis_connected_clients')"
echo ""

# ── Database ───────────────────────────────────────────────────────────────────
echo "=== DATABASE (RDS PostgreSQL) ==="
echo "  Active connections:      $(prom_scalar 'pg_stat_database_numbackends{datname="albumstore"}')"
echo "  Inserts/s:               $(prom_scalar 'rate(pg_stat_database_tup_inserted{datname="albumstore"}[5m])')"
echo "  Fetches/s:               $(prom_scalar 'rate(pg_stat_database_tup_fetched{datname="albumstore"}[5m])')"
echo "  RDS CPU (%):             $(prom_scalar 'aws_rds_cpuutilization_average')"
echo ""

# ── EC2 Node Metrics ───────────────────────────────────────────────────────────
echo "=== EC2 NODES (per node) ==="
echo "  CPU utilization:"
prom_query "aws_ec2_cpuutilization_average" "    CPU %"
echo "  Network in (bytes/s):"
prom_query "rate(aws_ec2_network_in_sum[5m])" "    Net In"
echo ""

# ── S3 Storage ─────────────────────────────────────────────────────────────────
echo "=== S3 STORAGE ==="
echo "  Total objects:           $(prom_scalar 's3_bucket_objects_total')"
SIZE=$(prom_scalar 's3_bucket_size_bytes')
echo "  Total size (bytes):      ${SIZE}"
echo ""

# ── Analytics Aggregation (from Kafka pipeline) ───────────────────────────────
echo "=== KAFKA ANALYTICS (latest window) ==="
echo "  Avg request rate/s:      $(prom_scalar 'analytics_request_rate_per_sec')"
echo "  Avg latency (ms):        $(prom_scalar 'analytics_avg_latency_ms')"
echo "  Cache hit rate (%):      $(prom_scalar 'analytics_cache_hit_rate_pct')"
echo "  Window size (events):    $(prom_scalar 'analytics_window_size')"
echo ""

# ── Active Alerts ──────────────────────────────────────────────────────────────
echo "=== ACTIVE ALERTS ==="
echo "  HIGH_LATENCY:            $(prom_scalar 'alert_active{type="HIGH_LATENCY"}')"
echo "  HIGH_ERROR_RATE:         $(prom_scalar 'alert_active{type="HIGH_ERROR_RATE"}')"
echo "  LOW_CACHE_HIT:           $(prom_scalar 'alert_active{type="LOW_CACHE_HIT"}')"
echo "  HIGH_CPU:                $(prom_scalar 'alert_active{type="HIGH_CPU"}')"
echo "  Total alerts fired:      $(prom_scalar 'sum(alert_fired_total)')"
echo ""

# ── Recent Alert Log ───────────────────────────────────────────────────────────
if [ -n "$MONITOR_ID" ]; then
  echo "=== RECENT ALERT LOG (last 10 entries) ==="
  aws ssm send-command \
    --instance-ids "$MONITOR_ID" \
    --document-name "AWS-RunShellScript" \
    --parameters 'commands=["docker logs alert 2>&1 | grep ALERT | tail -10"]' \
    --query 'Command.CommandId' --output text > /tmp/ssm_cmd.txt 2>/dev/null
  CMD_ID=$(cat /tmp/ssm_cmd.txt)
  sleep 8
  aws ssm get-command-invocation --command-id "$CMD_ID" \
    --instance-id "$MONITOR_ID" \
    --query 'StandardOutputContent' --output text 2>/dev/null | sed 's/^/  /'
  echo ""
fi

# ── Recommendation Prompt ─────────────────────────────────────────────────────
echo "════════════════════════════════════════════════════════════════════"
echo "CLAUDE CODE DEVOPS ANALYSIS REQUEST:"
echo ""
echo "Current infrastructure: ${NODES} × c5n.large EC2 nodes [min=1, max=4]"
echo "Scaling bounds: ±1 node per action, 2-minute cooldown between actions"
echo ""
echo "Based on the metrics above, please:"
echo "  1. Assess current system health (latency, errors, cache, DB)"
echo "  2. Identify any bottlenecks or concerning trends"
echo "  3. Recommend: scale_up / scale_down / no_action"
echo "  4. If scaling: specify exact node count [1–4] and reasoning"
echo "  5. If action needed, run: cd final_mastery/terraform && terraform apply -var='node_count=N'"
echo "════════════════════════════════════════════════════════════════════"
