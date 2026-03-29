#!/usr/bin/env bash
set -euo pipefail

# Deploy all services to AWS ECS
# Usage: ./scripts/deploy.sh [--skip-infra] [--skip-build]

REGION="us-west-2"
PROJECT="cs6650-final"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
TF_DIR="$PROJECT_DIR/terraform"

SKIP_INFRA=false
SKIP_BUILD=false

for arg in "$@"; do
  case $arg in
    --skip-infra) SKIP_INFRA=true ;;
    --skip-build) SKIP_BUILD=true ;;
  esac
done

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
ECR_BASE="$ACCOUNT_ID.dkr.ecr.$REGION.amazonaws.com"

echo "=== Deploy: account=$ACCOUNT_ID region=$REGION ==="

# ── Step 1: Terraform (creates ECR repos, VPC, ECS cluster, etc.) ──
if [ "$SKIP_INFRA" = false ]; then
  echo ""
  echo ">>> Step 1: Terraform init + apply..."
  cd "$TF_DIR"
  terraform init -input=false
  terraform apply -auto-approve -input=false
  cd "$PROJECT_DIR"
else
  echo ">>> Skipping Terraform (--skip-infra)"
fi

# ── Step 2: Build & push Docker images to ECR ──
if [ "$SKIP_BUILD" = false ]; then
  echo ""
  echo ">>> Step 2: Logging into ECR..."
  aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "$ECR_BASE"

  declare -A SERVICES=(
    ["cart-api"]="$ECR_BASE/$PROJECT/cart-api:latest"
    ["analytics"]="$ECR_BASE/$PROJECT/analytics:latest"
    ["alert"]="$ECR_BASE/$PROJECT/alert:latest"
  )

  for svc in "${!SERVICES[@]}"; do
    echo ""
    echo ">>> Building $svc..."
    docker build -t "$svc" "$PROJECT_DIR/services/$svc"
    docker tag "$svc" "${SERVICES[$svc]}"
    echo ">>> Pushing $svc to ECR..."
    docker push "${SERVICES[$svc]}"
  done
else
  echo ">>> Skipping build (--skip-build)"
fi

# ── Step 3: Force new deployment to pick up latest images ──
echo ""
echo ">>> Step 3: Forcing new ECS deployments..."
CLUSTER="$PROJECT-cluster"

for svc in cart-api analytics alert; do
  echo "  Updating $PROJECT-$svc..."
  aws ecs update-service \
    --cluster "$CLUSTER" \
    --service "$PROJECT-$svc" \
    --force-new-deployment \
    --region "$REGION" \
    --no-cli-pager > /dev/null
done

# ── Step 4: Wait for services to stabilize ──
echo ""
echo ">>> Step 4: Waiting for services to stabilize (this may take a few minutes)..."
for svc in kafka cart-api analytics alert; do
  echo "  Waiting for $PROJECT-$svc..."
  aws ecs wait services-stable \
    --cluster "$CLUSTER" \
    --services "$PROJECT-$svc" \
    --region "$REGION" 2>/dev/null || echo "  Warning: $PROJECT-$svc may not be stable yet"
done

# ── Done ──
ALB_DNS=$(cd "$TF_DIR" && terraform output -raw alb_dns_name 2>/dev/null || echo "unknown")
echo ""
echo "========================================="
echo " Deployment complete!"
echo " ALB: http://$ALB_DNS"
echo " Health: http://$ALB_DNS/health"
echo " Test:   curl -X POST http://$ALB_DNS/cart/items -H 'Content-Type: application/json' -d '{\"product_id\":1,\"quantity\":2,\"customer_id\":100}'"
echo "========================================="
