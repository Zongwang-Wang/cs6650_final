#!/bin/bash
# deploy.sh — Build Go binary and deploy to all EC2 nodes via SSM.
# Run AFTER terraform apply. The binary is pushed to S3 then pulled by each node.
#
# Usage:
#   cd ../server && ../terraform/deploy.sh
#   OR from any directory: /path/to/terraform/deploy.sh <path/to/server>
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="${1:-$SCRIPT_DIR/../server}"
cd "$SERVER_DIR"

# Get outputs from Terraform
TF_DIR="$SCRIPT_DIR"
S3_BUCKET=$(cd "$TF_DIR" && terraform output -raw s3_bucket 2>/dev/null)
INSTANCE_IDS=$(cd "$TF_DIR" && terraform output -json instance_ids 2>/dev/null | python3 -c "import sys,json; print(' '.join(json.load(sys.stdin)))")

if [ -z "$S3_BUCKET" ]; then
  echo "ERROR: Could not get S3 bucket from terraform output. Run 'terraform apply' first."
  exit 1
fi

echo "==> Building Linux amd64 binary..."
GOOS=linux GOARCH=amd64 go build -o album-store-server .
echo "    Binary: $(du -sh album-store-server | cut -f1)"

echo ""
echo "==> Uploading to s3://$S3_BUCKET/deploy/album-store-server ..."
aws s3 cp album-store-server "s3://$S3_BUCKET/deploy/album-store-server" --no-progress

echo ""
echo "==> Deploying to instances: $INSTANCE_IDS"
PIDS=()
for INST in $INSTANCE_IDS; do
  CMD_ID=$(aws ssm send-command \
    --instance-ids "$INST" \
    --document-name "AWS-RunShellScript" \
    --parameters "commands=[
      \"aws s3 cp s3://$S3_BUCKET/deploy/album-store-server /home/ec2-user/album-store/server --no-progress\",
      \"chmod +x /home/ec2-user/album-store/server\",
      \"chown ec2-user:ec2-user /home/ec2-user/album-store/server\",
      \"systemctl restart album-store || systemctl start album-store\",
      \"sleep 4\",
      \"curl -s http://localhost:8080/health\"
    ]" \
    --query 'Command.CommandId' --output text 2>/dev/null)
  echo "    $INST → SSM command: $CMD_ID"
  PIDS+=("$CMD_ID:$INST")
done

echo ""
echo "==> Waiting for deploys to complete..."
sleep 25
ALL_OK=true
for PAIR in "${PIDS[@]}"; do
  CMD_ID="${PAIR%%:*}"
  INST="${PAIR##*:}"
  STATUS=$(aws ssm get-command-invocation --command-id "$CMD_ID" --instance-id "$INST" \
    --query 'Status' --output text 2>/dev/null)
  OUTPUT=$(aws ssm get-command-invocation --command-id "$CMD_ID" --instance-id "$INST" \
    --query 'StandardOutputContent' --output text 2>/dev/null | grep -o '{"status":"ok"}' | head -1)
  if [ "$OUTPUT" = '{"status":"ok"}' ]; then
    echo "    ✅ $INST — healthy"
  else
    echo "    ❌ $INST — $STATUS (check: aws ssm get-command-invocation --command-id $CMD_ID --instance-id $INST)"
    ALL_OK=false
  fi
done

echo ""
ALB_URL=$(cd "$TF_DIR" && terraform output -raw alb_url 2>/dev/null)
if [ "$ALL_OK" = "true" ]; then
  echo "==> All nodes healthy. ALB: $ALB_URL"
  echo ""
  echo "==> Submit to ChaosArena:"
  cd "$TF_DIR" && terraform output -raw chaosarena_submit_cmd
else
  echo "==> Some nodes failed. Check SSM logs above."
  exit 1
fi
