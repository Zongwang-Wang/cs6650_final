#!/bin/bash
# deploy.sh — Full redeploy to EC2 via SSM (no SSH key needed).
# Run this after provisioning a fresh EC2 + RDS + S3 + Redis.
# Update INSTANCE_ID, S3_BUCKET, and EC2_IP for the new session.
set -e

INSTANCE_ID="${1:-REPLACE_WITH_NEW_INSTANCE_ID}"
S3_BUCKET="${2:-REPLACE_WITH_NEW_S3_BUCKET}"
EC2_IP="${3:-REPLACE_WITH_NEW_EC2_IP}"

if [[ "$INSTANCE_ID" == REPLACE* ]]; then
  echo "Usage: ./deploy.sh <instance-id> <s3-bucket> <ec2-ip>"
  echo "Example: ./deploy.sh i-0abc123 album-store-330214284546-us-west-2 1.2.3.4"
  exit 1
fi

echo "==> Building Linux binary..."
GOOS=linux GOARCH=amd64 go build -o album-store-server .
echo "    Binary: $(du -sh album-store-server | cut -f1)"

echo "==> Uploading to S3..."
aws s3 cp album-store-server "s3://$S3_BUCKET/deploy/album-store-server" --no-progress

echo "==> Deploying via SSM..."
CMD_ID=$(aws ssm send-command \
  --instance-ids "$INSTANCE_ID" \
  --document-name "AWS-RunShellScript" \
  --parameters "commands=[
    \"aws s3 cp s3://$S3_BUCKET/deploy/album-store-server /home/ec2-user/album-store/server --no-progress\",
    \"chmod +x /home/ec2-user/album-store/server\",
    \"systemctl restart album-store\",
    \"sleep 3\",
    \"curl -s http://localhost:8080/health\"
  ]" \
  --query 'Command.CommandId' --output text)

echo "    SSM command: $CMD_ID"
sleep 20

RESULT=$(aws ssm get-command-invocation \
  --command-id "$CMD_ID" \
  --instance-id "$INSTANCE_ID" \
  --query '[Status,StandardOutputContent]' \
  --output text 2>&1)

echo "    $RESULT" | tail -3

echo ""
echo "==> Smoke test..."
curl -s "http://$EC2_IP:8080/health" && echo ""
echo "==> Live at: http://$EC2_IP:8080"
echo ""
echo "==> Submit when ready:"
echo "    curl -s -X POST http://chaosarena-alb-938452724.us-west-2.elb.amazonaws.com/submit \\"
echo "      -H 'Content-Type: application/json' \\"
echo "      -d '{\"email\":\"wang.zongw@northeastern.edu\",\"nickname\":\"Zongwang Wang\",\"base_url\":\"http://$EC2_IP:8080\",\"contract\":\"v1-album-store\"}'"
