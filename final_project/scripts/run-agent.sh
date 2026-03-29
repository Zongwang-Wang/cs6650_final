#!/usr/bin/env bash
set -euo pipefail

# Run the AI DevOps Agent locally.
# Usage:
#   ./scripts/run-agent.sh              # dry-run mode (default)
#   ./scripts/run-agent.sh --live       # live mode (actually applies Terraform)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
AGENT_DIR="$PROJECT_DIR/services/ai-agent"
TF_DIR="$PROJECT_DIR/terraform"

# Load .env
if [ -f "$AGENT_DIR/.env" ]; then
  set -a
  source "$AGENT_DIR/.env"
  set +a
fi

# Override DRY_RUN if --live flag
DRY_RUN="${DRY_RUN:-true}"
for arg in "$@"; do
  case $arg in
    --live) DRY_RUN="false" ;;
    --dry)  DRY_RUN="true" ;;
  esac
done

# Auto-detect Kafka NLB from terraform output if not set
if [ -z "${KAFKA_BROKERS:-}" ]; then
  echo "Detecting Kafka NLB from Terraform..."
  KAFKA_NLB=$(cd "$TF_DIR" && terraform output -raw kafka_nlb_dns_name 2>/dev/null)
  KAFKA_BROKERS="${KAFKA_NLB}:9092"
fi

# Validate API key
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "ERROR: ANTHROPIC_API_KEY not set. Add it to $AGENT_DIR/.env"
  exit 1
fi

# Ensure Terraform is initialized
if [ ! -d "$TF_DIR/.terraform" ]; then
  echo "Initializing Terraform..."
  cd "$TF_DIR" && terraform init -input=false
fi

# Build the agent
echo "Building AI agent..."
cd "$AGENT_DIR" && go build -o ./ai-agent .

export ANTHROPIC_API_KEY
export KAFKA_BROKERS
export TERRAFORM_DIR="$TF_DIR"
export DRY_RUN
export COOLDOWN_SECONDS="${COOLDOWN_SECONDS:-60}"
export PORT="${PORT:-8083}"
export INSTANCE_ID="${INSTANCE_ID:-ai-agent-local}"
export KAFKA_CONSUMER_TOPIC="${KAFKA_CONSUMER_TOPIC:-analytics-output-topic}"
export KAFKA_PRODUCER_TOPIC="${KAFKA_PRODUCER_TOPIC:-infra-events-topic}"
export CONSUMER_GROUP="${CONSUMER_GROUP:-ai-agent-group}"

echo ""
echo "========================================="
echo "  AI DevOps Agent"
echo "  Mode: $([ "$DRY_RUN" = "true" ] && echo "DRY RUN" || echo "LIVE")"
echo "  Kafka: $KAFKA_BROKERS"
echo "  Terraform: $TERRAFORM_DIR"
echo "  Cooldown: ${COOLDOWN_SECONDS}s"
echo "  Status: http://localhost:${PORT}/status"
echo "========================================="
echo ""

exec "$AGENT_DIR/ai-agent"
