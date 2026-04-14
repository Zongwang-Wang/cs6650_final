#!/usr/bin/env bash
# run-monitor.sh — Start the Album Store Claude Code DevOps Monitor.
#
# This monitor polls Prometheus every 30s and invokes Claude Code (this very
# Claude Code session) when thresholds are breached or on a periodic schedule.
# Claude Code then analyzes the metrics and provides optimization suggestions.
#
# Usage:
#   ./run-monitor.sh                  # live mode — invokes Claude Code
#   ./run-monitor.sh --dry-run        # just prints the prompt, no Claude
#   ./run-monitor.sh --periodic=0     # alerts only, no periodic reviews
#
# Prerequisites:
#   - claude CLI in PATH (already available in this Claude Code session)
#   - Prometheus running at http://52.13.41.234:9090

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MONITOR_BIN="$SCRIPT_DIR/services/monitor/monitor"
TERRAFORM_DIR="$SCRIPT_DIR/../../../final_mastery/terraform"

# Build if binary doesn't exist or is stale
if [ ! -f "$MONITOR_BIN" ] || [ "$SCRIPT_DIR/services/monitor/main.go" -nt "$MONITOR_BIN" ]; then
  echo "Building monitor..."
  cd "$SCRIPT_DIR/services/monitor"
  go build -o monitor .
  cd "$SCRIPT_DIR"
fi

# Check claude CLI
if [[ "${1:-}" != "--dry-run" ]]; then
  if ! command -v claude &>/dev/null; then
    echo "ERROR: 'claude' CLI not found. Run with --dry-run to test without it."
    exit 1
  fi
fi

echo "=============================================="
echo "  Album Store DevOps Monitor"
echo "  Prometheus: http://52.13.41.234:9090"
echo "  Grafana:    http://52.13.41.234:3000"
echo "  Terraform:  $TERRAFORM_DIR"
echo "  Mode: $(echo "${1:-}" | grep -q dry-run && echo 'DRY RUN' || echo 'LIVE — Claude Code as DevOps agent')"
echo "=============================================="
echo ""

exec "$MONITOR_BIN" \
  --prometheus="http://52.13.41.234:9090" \
  --terraform-dir="$TERRAFORM_DIR" \
  --poll=30s \
  --cooldown=10m \
  --periodic=30m \
  "$@"
