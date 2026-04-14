#!/bin/bash
# deploy.sh — Launch a t3.micro EC2 instance and start the monitoring stack.
# Cost: ~$0.01/hr for the instance + negligible CloudWatch API calls.
#
# Prerequisites:
#   1. Album store infra is running (terraform applied in final_mastery/terraform/)
#   2. .env file is filled in from .env.example
#   3. AWS credentials active (lab session open)
#
# Usage:
#   cd album-store-monitor/
#   cp .env.example .env && vim .env   # fill in Redis, RDS, ALB endpoints
#   ./deploy.sh

set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Config ─────────────────────────────────────────────────────────────────────
INSTANCE_TYPE="${MONITOR_INSTANCE_TYPE:-t3.micro}"
REGION="${AWS_REGION:-us-west-2}"
SG_NAME="album-store-monitor-sg"
BUCKET="${MONITOR_S3_BUCKET:-}"   # optional: override s3 bucket for upload

echo "==> Album Store Monitor Deploy"
echo "    Instance: $INSTANCE_TYPE (~\$0.01/hr)"
echo "    Region:   $REGION"
echo ""

# ── Source .env ────────────────────────────────────────────────────────────────
if [ ! -f "$SCRIPT_DIR/.env" ]; then
  echo "ERROR: .env not found. Copy .env.example and fill in the endpoints."
  exit 1
fi
source "$SCRIPT_DIR/.env"

# ── Security Group ─────────────────────────────────────────────────────────────
VPC_ID=$(aws ec2 describe-vpcs --filters "Name=isDefault,Values=true" \
  --query 'Vpcs[0].VpcId' --output text --region "$REGION")

SG_ID=$(aws ec2 describe-security-groups \
  --filters "Name=group-name,Values=$SG_NAME" "Name=vpc-id,Values=$VPC_ID" \
  --query 'SecurityGroups[0].GroupId' --output text --region "$REGION" 2>/dev/null || echo "None")

if [ "$SG_ID" = "None" ] || [ -z "$SG_ID" ]; then
  echo "==> Creating security group..."
  SG_ID=$(aws ec2 create-security-group \
    --group-name "$SG_NAME" \
    --description "Album store monitoring (Prometheus + Grafana)" \
    --vpc-id "$VPC_ID" \
    --query 'GroupId' --output text --region "$REGION")
  aws ec2 authorize-security-group-ingress --group-id "$SG_ID" \
    --protocol tcp --port 22 --cidr 0.0.0.0/0 --region "$REGION" > /dev/null
  aws ec2 authorize-security-group-ingress --group-id "$SG_ID" \
    --protocol tcp --port 3000 --cidr 0.0.0.0/0 --region "$REGION" > /dev/null  # Grafana
  aws ec2 authorize-security-group-ingress --group-id "$SG_ID" \
    --protocol tcp --port 9090 --cidr 0.0.0.0/0 --region "$REGION" > /dev/null  # Prometheus
  echo "    SG: $SG_ID"
fi

# ── Bundle monitor files to S3 ─────────────────────────────────────────────────
# Pack the monitor config into a tarball and upload to the album store S3 bucket
# (reusing existing bucket — saves cost)
if [ -z "$BUCKET" ]; then
  ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
  BUCKET="album-store-${ACCOUNT}-${REGION}"
fi

echo "==> Uploading monitor config to s3://$BUCKET/monitor/..."
cd "$SCRIPT_DIR"
tar czf /tmp/monitor.tar.gz \
  docker-compose.yml \
  prometheus/ \
  grafana/ \
  .env
aws s3 cp /tmp/monitor.tar.gz "s3://$BUCKET/monitor/monitor.tar.gz" --no-progress
rm /tmp/monitor.tar.gz
echo "    Uploaded."

# ── User data ──────────────────────────────────────────────────────────────────
USER_DATA=$(cat << EOF
#!/bin/bash
exec > /var/log/monitor-setup.log 2>&1
set -e

# Install Docker
yum update -y
yum install -y docker
systemctl enable docker
systemctl start docker

# Install Docker Compose v2
mkdir -p /usr/local/lib/docker/cli-plugins
curl -fsSL https://github.com/docker/compose/releases/download/v2.27.0/docker-compose-linux-x86_64 \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

# Add ec2-user to docker group
usermod -aG docker ec2-user

# Download monitor config from S3
mkdir -p /home/ec2-user/monitor
aws s3 cp s3://$BUCKET/monitor/monitor.tar.gz /tmp/monitor.tar.gz
tar xzf /tmp/monitor.tar.gz -C /home/ec2-user/monitor/
chown -R ec2-user:ec2-user /home/ec2-user/monitor

# Start monitoring stack
cd /home/ec2-user/monitor
docker compose up -d

echo "Monitor stack started"
EOF
)

# ── Launch Instance ────────────────────────────────────────────────────────────
AMI=$(aws ec2 describe-images --owners amazon \
  --filters "Name=name,Values=al2023-ami-2023*-x86_64" "Name=state,Values=available" \
  --query 'sort_by(Images,&CreationDate)[-1].ImageId' --output text --region "$REGION")

echo "==> Launching $INSTANCE_TYPE monitor instance..."
INSTANCE_ID=$(aws ec2 run-instances \
  --image-id "$AMI" \
  --instance-type "$INSTANCE_TYPE" \
  --security-group-ids "$SG_ID" \
  --user-data "$USER_DATA" \
  --iam-instance-profile Name=LabInstanceProfile \
  --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=album-store-monitor}]" \
  --query 'Instances[0].InstanceId' --output text --region "$REGION")

echo "    Instance: $INSTANCE_ID"
echo "    Waiting for it to start..."
aws ec2 wait instance-running --instance-ids "$INSTANCE_ID" --region "$REGION"

PUBLIC_IP=$(aws ec2 describe-instances --instance-ids "$INSTANCE_ID" \
  --query 'Reservations[0].Instances[0].PublicIpAddress' --output text --region "$REGION")

echo ""
echo "╔══════════════════════════════════════════════════════╗"
echo "║  Monitor is starting (Docker setup takes ~2 min)    ║"
echo "╠══════════════════════════════════════════════════════╣"
echo "║  Grafana:     http://$PUBLIC_IP:3000              ║"
echo "║  Login:       admin / ${GRAFANA_ADMIN_PASSWORD:-admin}                    ║"
echo "║  Prometheus:  http://$PUBLIC_IP:9090              ║"
echo "╠══════════════════════════════════════════════════════╣"
echo "║  Instance: $INSTANCE_ID              ║"
echo "║  Cost: ~\$0.01/hr (t3.micro)                        ║"
echo "╚══════════════════════════════════════════════════════╝"
echo ""
echo "Teardown:  ./teardown.sh $INSTANCE_ID"
