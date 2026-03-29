#!/usr/bin/env bash
set -euo pipefail

# Run the local monitor that watches Kafka metrics and invokes Claude Code on alerts.
#
# Usage:
#   ./scripts/run-monitor.sh              # live mode (invokes Claude Code)
#   ./scripts/run-monitor.sh --dry-run    # just prints the prompt, doesn't invoke Claude

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
MONITOR_DIR="$PROJECT_DIR/services/monitor"
TF_DIR="$PROJECT_DIR/terraform"

# Auto-detect Kafka NLB from terraform
KAFKA_NLB=$(cd "$TF_DIR" && terraform output -raw kafka_nlb_dns_name 2>/dev/null || echo "")
if [ -z "$KAFKA_NLB" ]; then
  echo "ERROR: Could not detect Kafka NLB. Is terraform applied?"
  exit 1
fi

BROKERS="${KAFKA_NLB}:9092"

# Check claude CLI is available
if ! command -v claude &>/dev/null; then
  echo "ERROR: 'claude' CLI not found in PATH"
  exit 1
fi

# Build
echo "Building monitor..."
cd "$MONITOR_DIR" && go build -o ./monitor .

echo ""
echo "========================================="
echo "  Infrastructure Monitor"
echo "  Kafka: $BROKERS"
echo "  Terraform: $TF_DIR"
echo "  Mode: $(echo "$@" | grep -q dry-run && echo "DRY RUN" || echo "LIVE — will invoke Claude Code")"
echo "========================================="
echo ""

exec "$MONITOR_DIR/monitor" \
  --brokers="$BROKERS" \
  --terraform-dir="$TF_DIR" \
  --cooldown=120 \
  "$@"
