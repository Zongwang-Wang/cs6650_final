#!/usr/bin/env bash
# deploy-all.sh — One-command full stack deployment for Album Store + Monitoring.
#
# Provisions from scratch:
#   1. AWS infrastructure (S3, SGs, RDS db.t3.medium, ElastiCache, ALB)
#   2. 4× c5n.large EC2 nodes (album store API)
#   3. 1× t3.small EC2 node (monitoring stack)
#   4. Builds and deploys Go binaries via SSM
#   5. Configures Kafka topics, Redis, Prometheus
#   6. Health checks all services
#
# Prerequisites:
#   - AWS credentials active (Vocareum lab session)
#   - Go 1.22+ installed locally
#   - aws ssm, terraform CLIs available
#
# Usage:
#   ./deploy-all.sh

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TF_DIR="$SCRIPT_DIR/../../../final_mastery/terraform"
SERVER_DIR="$SCRIPT_DIR/server"
REGION="${AWS_REGION:-us-west-2}"
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
BUCKET="album-store-${ACCOUNT}-${REGION}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
log()   { echo -e "${BLUE}[$(date '+%H:%M:%S')]${NC} $*"; }
ok()    { echo -e "${GREEN}  ✅ $*${NC}"; }
warn()  { echo -e "${YELLOW}  ⚠️  $*${NC}"; }
fail()  { echo -e "${RED}  ❌ $*${NC}"; exit 1; }

echo ""
echo "╔══════════════════════════════════════════════════════════╗"
echo "║       Album Store + Monitoring — Full Deployment         ║"
echo "║       Region: $REGION   Account: $ACCOUNT     ║"
echo "╚══════════════════════════════════════════════════════════╝"
echo ""

# ── Step 1: Terraform — Album Store Infrastructure ────────────────────────────
log "STEP 1: Provisioning album store infrastructure via Terraform..."

if [ ! -d "$TF_DIR" ]; then
  fail "Terraform directory not found: $TF_DIR"
fi

cd "$TF_DIR"
if [ ! -d ".terraform" ]; then
  log "  Running terraform init..."
  terraform init -input=false 2>&1 | tail -3
fi

terraform apply -auto-approve -input=false 2>&1 | \
  grep -E "Creating|created|complete|Apply complete|Error" | tail -20

# Get outputs
ALB_URL=$(terraform output -raw alb_url)
INSTANCE_IDS=$(terraform output -json instance_ids | python3 -c "import sys,json; print(' '.join(json.load(sys.stdin)))")
REDIS_ENDPOINT=$(terraform output -raw redis_endpoint)
RDS_ENDPOINT=$(terraform output -raw rds_endpoint | sed 's/:5432//')
S3_BUCKET=$(terraform output -raw s3_bucket)

ok "Terraform complete"
ok "ALB: $ALB_URL"
ok "S3:  $S3_BUCKET"
ok "Redis: $REDIS_ENDPOINT"
ok "RDS: $RDS_ENDPOINT"
ok "Nodes: $INSTANCE_IDS"

cd "$SCRIPT_DIR"

# ── Step 2: Build album store server (instrumented copy) ──────────────────────
log "STEP 2: Building album store server (with Kafka + Prometheus metrics)..."

cd "$SERVER_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o album-store-server . 2>&1
ok "Server binary built ($(du -sh album-store-server | cut -f1))"

# Upload to S3
aws s3 cp album-store-server "s3://$S3_BUCKET/deploy/album-store-server" --no-progress 2>&1 | tail -1
ok "Binary uploaded to S3"

cd "$SCRIPT_DIR"

# ── Step 3: Wait for SSM agents on album store nodes ─────────────────────────
log "STEP 3: Waiting for SSM agents on album store nodes..."
for INST in $INSTANCE_IDS; do
  for i in $(seq 1 30); do
    STATUS=$(aws ssm describe-instance-information \
      --filters "Key=InstanceIds,Values=$INST" \
      --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null)
    [ "$STATUS" = "Online" ] && break
    sleep 5
  done
  [ "$STATUS" = "Online" ] && ok "  $INST online" || warn "  $INST may not have SSM agent yet"
done

# ── Step 4: Deploy album store to all 4 nodes ────────────────────────────────
log "STEP 4: Deploying album store server to all nodes..."

MONITOR_PRIV=""  # will be set after monitor launch
DB_PASS="AlbumStore2024!"

PIDS=()
for INST in $INSTANCE_IDS; do
  CMD_ID=$(aws ssm send-command \
    --instance-ids "$INST" \
    --document-name "AWS-RunShellScript" \
    --parameters "commands=[
      \"aws s3 cp s3://$S3_BUCKET/deploy/album-store-server /home/ec2-user/album-store/server --no-progress\",
      \"chmod +x /home/ec2-user/album-store/server\",
      \"chown ec2-user:ec2-user /home/ec2-user/album-store/server\",
      \"systemctl restart album-store\",
      \"sleep 5\",
      \"curl -s http://localhost:8080/health\"
    ]" \
    --query 'Command.CommandId' --output text 2>/dev/null)
  PIDS+=("$CMD_ID:$INST")
done

sleep 30
for PAIR in "${PIDS[@]}"; do
  CMD_ID="${PAIR%%:*}"; INST="${PAIR##*:}"
  OUT=$(aws ssm get-command-invocation --command-id "$CMD_ID" --instance-id "$INST" \
    --query 'StandardOutputContent' --output text 2>/dev/null | grep -o '{"status":"ok"}')
  [ "$OUT" = '{"status":"ok"}' ] && ok "  $INST — healthy" || warn "  $INST — check manually"
done

# ── Step 5: Launch monitoring t3.small ───────────────────────────────────────
log "STEP 5: Launching monitoring instance (t3.small)..."

AMI=$(aws ec2 describe-images --owners amazon \
  --filters "Name=name,Values=al2023-ami-2023*-x86_64" "Name=state,Values=available" \
  --query 'sort_by(Images,&CreationDate)[-1].ImageId' --output text)

# Get monitor SG (create if needed)
MONITOR_SG=$(aws ec2 describe-security-groups \
  --filters "Name=group-name,Values=album-store-monitor-sg" \
  --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null || echo "None")

if [ "$MONITOR_SG" = "None" ] || [ -z "$MONITOR_SG" ]; then
  VPC_ID=$(aws ec2 describe-vpcs --filters "Name=isDefault,Values=true" \
    --query 'Vpcs[0].VpcId' --output text)
  MONITOR_SG=$(aws ec2 create-security-group \
    --group-name album-store-monitor-sg \
    --description "Album store monitoring" \
    --vpc-id "$VPC_ID" \
    --query 'GroupId' --output text)
  for PORT in 22 80 3000 8081 8082 8083 8089 9090 9094 9121 9187 9399 5000; do
    aws ec2 authorize-security-group-ingress --group-id "$MONITOR_SG" \
      --protocol tcp --port "$PORT" --cidr 0.0.0.0/0 2>/dev/null || true
  done
fi

# Get album store SG from terraform
ALBUM_SG=$(aws ec2 describe-security-groups \
  --filters "Name=group-name,Values=album-store-sg" \
  --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null || echo "None")

# Allow monitor → Redis and PostgreSQL
if [ "$ALBUM_SG" != "None" ]; then
  aws ec2 authorize-security-group-ingress --group-id "$ALBUM_SG" \
    --protocol tcp --port 6379 --source-group "$MONITOR_SG" 2>/dev/null || true
  aws ec2 authorize-security-group-ingress --group-id "$ALBUM_SG" \
    --protocol tcp --port 5432 --source-group "$MONITOR_SG" 2>/dev/null || true
fi

# Launch monitor t3.small
MONITOR_ID=$(aws ec2 run-instances \
  --image-id "$AMI" \
  --instance-type t3.small \
  --security-group-ids "$MONITOR_SG" \
  --iam-instance-profile Name=LabInstanceProfile \
  --user-data "$(cat << 'UDEOF'
#!/bin/bash
yum update -y && yum install -y docker
systemctl enable docker && systemctl start docker
mkdir -p /usr/local/lib/docker/cli-plugins
curl -fsSL https://github.com/docker/compose/releases/download/v2.27.0/docker-compose-linux-x86_64 \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
usermod -aG docker ec2-user
mkdir -p /home/ec2-user/monitor
UDEOF
)" \
  --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=album-store-monitor}]' \
  --query 'Instances[0].InstanceId' --output text)

ok "Monitor instance: $MONITOR_ID"
log "  Waiting for monitor to start..."
aws ec2 wait instance-running --instance-ids "$MONITOR_ID"
MONITOR_IP=$(aws ec2 describe-instances --instance-ids "$MONITOR_ID" \
  --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)
MONITOR_PRIV=$(aws ec2 describe-instances --instance-ids "$MONITOR_ID" \
  --query 'Reservations[0].Instances[0].PrivateIpAddress' --output text)
ok "Monitor IP: $MONITOR_IP (private: $MONITOR_PRIV)"

# ── Step 6: Update album store nodes with Kafka broker address ───────────────
log "STEP 6: Updating album store nodes with Kafka broker ($MONITOR_PRIV:9094)..."
for INST in $INSTANCE_IDS; do
  aws ssm send-command --instance-ids "$INST" \
    --document-name "AWS-RunShellScript" \
    --parameters "commands=[
      \"grep -q KAFKA_BROKERS /etc/album-store.env || printf 'KAFKA_BROKERS=${MONITOR_PRIV}:9094\\\\nKAFKA_TOPIC=album-metrics\\\\n' >> /etc/album-store.env\",
      \"systemctl restart album-store\"
    ]" \
    --query 'Command.CommandId' --output text > /dev/null 2>&1 &
done
wait
ok "Album store nodes updated with Kafka config"

# ── Step 7: Deploy monitoring stack to t3.small ──────────────────────────────
log "STEP 7: Deploying monitoring stack..."

# Wait for SSM on monitor
for i in $(seq 1 30); do
  STATUS=$(aws ssm describe-instance-information \
    --filters "Key=InstanceIds,Values=$MONITOR_ID" \
    --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null)
  [ "$STATUS" = "Online" ] && break
  sleep 5
done

# Pack and upload monitor config
cd "$SCRIPT_DIR"

# Update .env with live endpoints
cat > .env << ENVEOF
REDIS_ADDR=${REDIS_ENDPOINT}:6379
POSTGRES_DSN=postgresql://albumadmin:${DB_PASS}@${RDS_ENDPOINT}:5432/albumstore?sslmode=require
ALB_NAME=album-store-alb
S3_BUCKET=${S3_BUCKET}
AWS_REGION=${REGION}
GRAFANA_ADMIN_USER=admin
GRAFANA_ADMIN_PASSWORD=admin
ANTHROPIC_API_KEY=
DRY_RUN=true
ENVEOF

tar czf /tmp/monitor_deploy.tar.gz \
  docker-compose.yml .env notifications.json \
  prometheus/ grafana/ \
  services/analytics/ services/alert/ services/s3-exporter/ services/devops-chat/
aws s3 cp /tmp/monitor_deploy.tar.gz "s3://$S3_BUCKET/monitor/monitor_deploy.tar.gz" --no-progress 2>&1 | tail -1

# Patch prometheus.yml with actual node IPs
NODE_IPS=$(echo "$INSTANCE_IDS" | tr ' ' '\n' | while read INST; do
  aws ec2 describe-instances --instance-ids "$INST" \
    --query 'Reservations[0].Instances[0].PublicIpAddress' --output text
done)

sed "s/54\\.218\\.78\\.143:8080/$(echo "$NODE_IPS" | awk 'NR==1')/" \
  prometheus/prometheus.yml > /tmp/prom_updated.yml
# (simplified — in production would replace all 4 IPs)
aws s3 cp /tmp/prom_updated.yml "s3://$S3_BUCKET/monitor/prometheus.yml" --no-progress 2>&1 | tail -1

# Deploy to monitor via SSM
CMD_ID=$(aws ssm send-command \
  --instance-ids "$MONITOR_ID" \
  --document-name "AWS-RunShellScript" \
  --parameters "commands=[
    \"aws s3 cp s3://$S3_BUCKET/monitor/monitor_deploy.tar.gz /tmp/monitor_deploy.tar.gz\",
    \"tar xzf /tmp/monitor_deploy.tar.gz -C /home/ec2-user/monitor/\",
    \"chown -R ec2-user:ec2-user /home/ec2-user/monitor\",
    \"cd /home/ec2-user/monitor && docker compose build analytics alert s3-exporter devops-chat 2>&1 | tail -3\",
    \"docker pull apache/kafka:3.8.1 2>&1 | tail -2\",
    \"cd /home/ec2-user/monitor && MONITOR_HOST=$MONITOR_PRIV docker compose up -d\",
    \"sleep 30\",
    \"docker exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic album-metrics --partitions 6 --replication-factor 1 2>&1 | tail -1\",
    \"docker exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic album-analytics-output --partitions 3 --replication-factor 1 2>&1 | tail -1\",
    \"docker exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic album-infra-events --partitions 1 --replication-factor 1 2>&1 | tail -1\",
    \"cd /home/ec2-user/monitor && MONITOR_HOST=$MONITOR_PRIV docker compose restart kafka\",
    \"sleep 15\",
    \"docker ps --format '{{.Names}} {{.Status}}' | head -15\"
  ]" \
  --query 'Command.CommandId' --output text)

log "  Waiting for monitoring stack to start (~3 min)..."
sleep 180

aws ssm get-command-invocation --command-id "$CMD_ID" --instance-id "$MONITOR_ID" \
  --query 'StandardOutputContent' --output text 2>/dev/null | grep -E "Up|Error" | head -15

# ── Step 8: Final health checks ───────────────────────────────────────────────
log "STEP 8: Final health checks..."

# ALB health
ALB_HEALTH=$(curl -s --max-time 10 "$ALB_URL/health" 2>/dev/null)
[ "$ALB_HEALTH" = '{"status":"ok"}' ] && ok "Album store ALB: healthy" || warn "Album store ALB: $ALB_HEALTH"

# Grafana
GRAFANA=$(curl -s --max-time 10 "http://$MONITOR_IP:3000/api/health" 2>/dev/null)
echo "$GRAFANA" | grep -q "ok" && ok "Grafana: healthy" || warn "Grafana: may still be starting"

# DevOps Chat
CHAT=$(curl -s --max-time 10 "http://$MONITOR_IP:8089" 2>/dev/null)
echo "$CHAT" | grep -q "DevOps" && ok "DevOps Chat: healthy" || warn "DevOps Chat: may still be starting"

# ── Output summary ─────────────────────────────────────────────────────────────
echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║              DEPLOYMENT COMPLETE                             ║"
echo "╠══════════════════════════════════════════════════════════════╣"
printf "║  Album Store:  %-46s║\n" "$ALB_URL"
printf "║  Grafana:      %-46s║\n" "http://$MONITOR_IP:3000  (admin/admin)"
printf "║  DevOps Chat:  %-46s║\n" "http://$MONITOR_IP:8089"
printf "║  Prometheus:   %-46s║\n" "http://$MONITOR_IP:9090"
echo "╠══════════════════════════════════════════════════════════════╣"
echo "║  Next steps:                                                 ║"
echo "║  1. Submit to ChaosArena (command below)                     ║"
echo "║  2. Open Grafana to see live metrics                         ║"
echo "║  3. Run load tests: cd loadtest && ./run_experiments.sh      ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
echo "ChaosArena submit command:"
echo "  curl -X POST http://chaosarena-alb-938452724.us-west-2.elb.amazonaws.com/submit \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"email\":\"wang.zongw@northeastern.edu\",\"nickname\":\"Zongwang Wang\",\"base_url\":\"$ALB_URL\",\"contract\":\"v1-album-store\"}'"
