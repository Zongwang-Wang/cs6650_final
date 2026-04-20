# CS6650 Final Project -- Setup Manual

**Team:** Zongwang Wang, Dylan Pan, Lucas Chen, Ellis Guo
**Last updated:** 2026-03-29

This manual walks you through deploying the entire distributed monitoring + AI DevOps agent system from scratch. Follow every step in order.

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Step 1: Clone and Build](#step-1-clone-and-build)
3. [Step 2: Deploy Infrastructure to AWS](#step-2-deploy-infrastructure-to-aws)
4. [Step 3: Start Local Monitoring (Grafana)](#step-3-start-local-monitoring-grafana)
5. [Step 4: Run Load Tests](#step-4-run-load-tests)
6. [Step 5: Run the AI Monitor (Claude Code Approach)](#step-5-run-the-ai-monitor-claude-code-approach)
7. [Step 6: Run the AI Agent (Claude API Approach, Optional)](#step-6-run-the-ai-agent-claude-api-approach-optional)
8. [Step 7: SNS Email Alerts](#step-7-sns-email-alerts)
9. [Step 8: Tear Down](#step-8-tear-down)
10. [Troubleshooting](#troubleshooting)
11. [Quick Reference](#quick-reference)

---

## Prerequisites

### Software Required

| Tool | Minimum Version | Install |
|------|----------------|---------|
| Go | 1.22+ | https://go.dev/dl/ |
| Docker + Docker Compose | 24+ / v2 | https://docs.docker.com/get-docker/ |
| Terraform | 1.3+ | https://developer.hashicorp.com/terraform/downloads |
| AWS CLI | v2 | https://docs.aws.amazon.com/cli/latest/userguide/install-cliv2.html |
| Python 3 + pip | 3.9+ | https://www.python.org/downloads/ |
| Locust | 2.20+ | `pip install locust` |
| Claude Code CLI | latest | `npm install -g @anthropic-ai/claude-code` (only needed for Step 5) |

### Verify Installations

Run each command and confirm you see version output:

```bash
go version
# Expected: go version go1.22.x linux/amd64

docker --version
# Expected: Docker version 24.x or newer

docker compose version
# Expected: Docker Compose version v2.x

terraform --version
# Expected: Terraform v1.3+ (any 1.x should work)

aws --version
# Expected: aws-cli/2.x.x

python3 --version
# Expected: Python 3.9+

locust --version
# Expected: locust 2.20+

claude --version
# Expected: some version string (only needed for Step 5)
```

### AWS Lab Account Setup

Our lab accounts use pre-existing credentials. Configure the AWS CLI:

```bash
aws configure
```

Enter the following when prompted:

```
AWS Access Key ID:     <your lab access key>
AWS Secret Access Key: <your lab secret key>
Default region name:   us-west-2
Default output format: json
```

If your lab provides a session token, also set it:

```bash
aws configure set aws_session_token <your-session-token>
```

Verify your identity:

```bash
aws sts get-caller-identity
```

You should see your account ID and `LabRole` or similar. Note the **Account ID** -- you will need it later (it looks like `330214284546`).

**Important lab account limitations:**
- You CANNOT create custom IAM roles (all ECS tasks use the pre-existing `LabRole`)
- You CANNOT use SES (we use SNS for email alerts instead)
- You CANNOT use Cloud Map / Service Discovery (we use NLB for Kafka instead)

---

## Step 1: Clone and Build

### 1.1 Clone the Repository

```bash
cd ~/cs6650
git clone <REPO_URL> final_project
cd final_project
```

If you already have the repo, just pull the latest:

```bash
cd ~/cs6650/final_project
git pull origin main
```

### 1.2 Build All Go Services

Each service has its own `go.mod`. You need to download dependencies and compile each one.

**Cart API:**

```bash
cd ~/cs6650/final_project/services/cart-api
go mod tidy
go build -o ./cart-api .
echo "cart-api build: OK"
```

**Analytics:**

```bash
cd ~/cs6650/final_project/services/analytics
go mod tidy
go build -o ./analytics .
echo "analytics build: OK"
```

**Alert:**

```bash
cd ~/cs6650/final_project/services/alert
go mod tidy
go build -o ./alert .
echo "alert build: OK"
```

**Monitor (for Step 5):**

```bash
cd ~/cs6650/final_project/services/monitor
go mod tidy
go build -o ./monitor .
echo "monitor build: OK"
```

**AI Agent (for Step 6, optional):**

```bash
cd ~/cs6650/final_project/services/ai-agent
go mod tidy
go build -o ./ai-agent .
echo "ai-agent build: OK"
```

If all five print "OK", your builds are good. You can delete the binaries -- Docker will rebuild them inside containers.

---

## Step 2: Deploy Infrastructure to AWS

This step creates everything on AWS: VPC, ALB, ECS cluster, Kafka, ECR repos, DynamoDB table, SNS topic, Prometheus, and auto-scaling.

### 2.1 Initialize and Apply Terraform

```bash
cd ~/cs6650/final_project/terraform
terraform init
terraform apply -auto-approve
```

This takes about 3-5 minutes. When it finishes, you will see outputs like:

```
alb_dns_name       = "cs6650-final-alb-XXXXXXXXX.us-west-2.elb.amazonaws.com"
kafka_nlb_dns_name = "cs6650-final-kafka-nlb-XXXXXXXX.elb.us-west-2.amazonaws.com"
prometheus_url     = "http://cs6650-final-prom-nlb-XXXXXXXX.elb.us-west-2.amazonaws.com:9090"
ecr_cart_api       = "ACCOUNT_ID.dkr.ecr.us-west-2.amazonaws.com/cs6650-final/cart-api"
ecr_analytics      = "ACCOUNT_ID.dkr.ecr.us-west-2.amazonaws.com/cs6650-final/analytics"
ecr_alert          = "ACCOUNT_ID.dkr.ecr.us-west-2.amazonaws.com/cs6650-final/alert"
ecr_prometheus     = "ACCOUNT_ID.dkr.ecr.us-west-2.amazonaws.com/cs6650-final/prometheus"
```

**Save these values!** You will need them in subsequent steps. You can retrieve them anytime with:

```bash
cd ~/cs6650/final_project/terraform
terraform output
```

Or get individual values:

```bash
terraform output -raw alb_dns_name
terraform output -raw kafka_nlb_dns_name
terraform output -raw prometheus_url
```

### 2.2 Log In to ECR

Replace `<ACCOUNT_ID>` with your AWS account ID (from `aws sts get-caller-identity`):

```bash
aws ecr get-login-password --region us-west-2 | docker login --username AWS --password-stdin <ACCOUNT_ID>.dkr.ecr.us-west-2.amazonaws.com
```

You should see `Login Succeeded`. This login expires after 12 hours.

### 2.3 Build and Push Docker Images

**Option A: Use the deploy script (recommended)**

```bash
cd ~/cs6650/final_project
./scripts/deploy.sh --skip-infra
```

This builds all three service images, pushes them to ECR, and forces new ECS deployments.

**Option B: Do it manually**

Set your ECR base URL:

```bash
export ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
export ECR_BASE="$ACCOUNT_ID.dkr.ecr.us-west-2.amazonaws.com"
```

Build, tag, and push each service:

```bash
# Cart API
docker build -t cart-api ~/cs6650/final_project/services/cart-api
docker tag cart-api $ECR_BASE/cs6650-final/cart-api:latest
docker push $ECR_BASE/cs6650-final/cart-api:latest

# Analytics
docker build -t analytics ~/cs6650/final_project/services/analytics
docker tag analytics $ECR_BASE/cs6650-final/analytics:latest
docker push $ECR_BASE/cs6650-final/analytics:latest

# Alert
docker build -t alert ~/cs6650/final_project/services/alert
docker tag alert $ECR_BASE/cs6650-final/alert:latest
docker push $ECR_BASE/cs6650-final/alert:latest
```

Build and push the Prometheus image (this bakes in the ECS scrape config):

```bash
docker build -t prometheus ~/cs6650/final_project/monitoring/prometheus
docker tag prometheus $ECR_BASE/cs6650-final/prometheus:v3
docker push $ECR_BASE/cs6650-final/prometheus:v3
```

**Important:** The Prometheus ECS scrape config (`monitoring/prometheus/prometheus-ecs.yml`) has the ALB DNS hardcoded. Before building the Prometheus image, update it with your current ALB DNS:

```bash
# Get your ALB DNS
ALB_DNS=$(cd ~/cs6650/final_project/terraform && terraform output -raw alb_dns_name)
echo "ALB DNS: $ALB_DNS"

# Edit monitoring/prometheus/prometheus-ecs.yml
# Replace the old ALB DNS in the targets line with your new ALB DNS
```

The file should look like:

```yaml
scrape_configs:
  - job_name: "cart-api"
    metrics_path: /metrics
    static_configs:
      - targets: ["<ALB_DNS>:80"]
        labels:
          service: "cart-api"
```

Then rebuild and push the Prometheus image.

### 2.4 Force Deploy ECS Services

If you used the deploy script (Option A), this is already done. Otherwise:

```bash
aws ecs update-service --cluster cs6650-final-cluster --service cs6650-final-cart-api --force-new-deployment --region us-west-2 --no-cli-pager > /dev/null
aws ecs update-service --cluster cs6650-final-cluster --service cs6650-final-analytics --force-new-deployment --region us-west-2 --no-cli-pager > /dev/null
aws ecs update-service --cluster cs6650-final-cluster --service cs6650-final-alert --force-new-deployment --region us-west-2 --no-cli-pager > /dev/null
aws ecs update-service --cluster cs6650-final-cluster --service cs6650-final-prometheus --force-new-deployment --region us-west-2 --no-cli-pager > /dev/null
```

### 2.5 Create Kafka Topics

Kafka on ECS has `auto.create.topics.enable=true`, so topics are created automatically when services start producing/consuming. However, if you want to create them explicitly with the right partition counts, you can run a one-off ECS task.

**Get the subnet ID and security group ID from AWS:**

```bash
# Get subnet IDs
aws ec2 describe-subnets --filters "Name=tag:Project,Values=cs6650-final" --query "Subnets[*].SubnetId" --output text --region us-west-2
# Example output: subnet-0abc123 subnet-0def456

# Get security group ID
aws ec2 describe-security-groups --filters "Name=group-name,Values=cs6650-final-ecs-tasks-sg" --query "SecurityGroups[0].GroupId" --output text --region us-west-2
# Example output: sg-0abc123def456
```

**Get the Kafka NLB DNS:**

```bash
KAFKA_NLB=$(cd ~/cs6650/final_project/terraform && terraform output -raw kafka_nlb_dns_name)
echo "Kafka NLB: $KAFKA_NLB"
```

**Register a one-off kafka-init task definition:**

```bash
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
KAFKA_NLB=$(cd ~/cs6650/final_project/terraform && terraform output -raw kafka_nlb_dns_name)

aws ecs register-task-definition \
  --family cs6650-final-kafka-init \
  --network-mode awsvpc \
  --requires-compatibilities FARGATE \
  --cpu 256 --memory 512 \
  --execution-role-arn "arn:aws:iam::${ACCOUNT_ID}:role/LabRole" \
  --task-role-arn "arn:aws:iam::${ACCOUNT_ID}:role/LabRole" \
  --container-definitions "[{
    \"name\": \"kafka-init\",
    \"image\": \"confluentinc/cp-kafka:7.5.0\",
    \"essential\": true,
    \"command\": [
      \"bash\", \"-c\",
      \"echo Waiting for Kafka... && sleep 10 && kafka-topics --bootstrap-server ${KAFKA_NLB}:9092 --create --if-not-exists --topic metrics-topic --partitions 6 --replication-factor 1 && kafka-topics --bootstrap-server ${KAFKA_NLB}:9092 --create --if-not-exists --topic analytics-output-topic --partitions 3 --replication-factor 1 && kafka-topics --bootstrap-server ${KAFKA_NLB}:9092 --create --if-not-exists --topic infra-events-topic --partitions 1 --replication-factor 1 && echo Done && kafka-topics --bootstrap-server ${KAFKA_NLB}:9092 --list\"
    ],
    \"logConfiguration\": {
      \"logDriver\": \"awslogs\",
      \"options\": {
        \"awslogs-group\": \"/ecs/cs6650-final/kafka\",
        \"awslogs-region\": \"us-west-2\",
        \"awslogs-stream-prefix\": \"kafka-init\"
      }
    }
  }]" \
  --region us-west-2 --no-cli-pager
```

**Run the task** (replace `<SUBNET_ID>` and `<SG_ID>` with your values from above):

```bash
aws ecs run-task \
  --cluster cs6650-final-cluster \
  --task-definition cs6650-final-kafka-init \
  --launch-type FARGATE \
  --network-configuration "awsvpcConfiguration={subnets=[<SUBNET_ID>],securityGroups=[<SG_ID>],assignPublicIp=ENABLED}" \
  --region us-west-2 --no-cli-pager
```

Check the CloudWatch logs at `/ecs/cs6650-final/kafka` (stream prefix `kafka-init`) to confirm topics were created.

### 2.6 Wait for Services to Stabilize

```bash
for svc in kafka cart-api analytics alert prometheus; do
  echo "Waiting for cs6650-final-$svc..."
  aws ecs wait services-stable \
    --cluster cs6650-final-cluster \
    --services "cs6650-final-$svc" \
    --region us-west-2 2>/dev/null || echo "  Warning: cs6650-final-$svc may not be stable yet"
done
```

This can take 3-5 minutes. The `wait` command times out after 10 minutes.

### 2.7 Verify Deployment

```bash
ALB_DNS=$(cd ~/cs6650/final_project/terraform && terraform output -raw alb_dns_name)

# Health check
curl -s "http://$ALB_DNS/health"
# Expected: {"status":"ok"} or similar

# Test a POST
curl -s -X POST "http://$ALB_DNS/cart/items" \
  -H "Content-Type: application/json" \
  -d '{"product_id":1,"quantity":2,"customer_id":100}'
# Expected: 201 response with cart_id
```

If the health check returns a response, your deployment is working.

---

## Step 3: Start Local Monitoring (Grafana)

Grafana runs locally on your machine and connects to:
- A **local Prometheus** instance that scrapes the remote ALB (for cart-api metrics) and the local AI agent
- The **remote Prometheus on ECS** (for ECS-side metrics)

### 3.1 Update Config Files with Current DNS Values

After every `terraform destroy` and `terraform apply`, the NLB/ALB DNS names change. You must update two files.

**File 1: `monitoring/prometheus/prometheus-local-agent.yml`**

Open this file and replace the ALB DNS in the `cart-api` target:

```yaml
scrape_configs:
  # Local AI agent
  - job_name: "ai-agent"
    static_configs:
      - targets: ["host.docker.internal:8083"]

  # Remote cart-api via ALB (update DNS after terraform apply)
  - job_name: "cart-api"
    metrics_path: /metrics
    static_configs:
      - targets: ["<ALB_DNS>:80"]
```

Replace `<ALB_DNS>` with the output of:

```bash
cd ~/cs6650/final_project/terraform && terraform output -raw alb_dns_name
```

**File 2: `monitoring/grafana/datasources-combined.yml`**

Open this file and replace the Prometheus NLB DNS in the `Prometheus-ECS` datasource:

```yaml
datasources:
  # Local Prometheus
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus-local:9090
    isDefault: true
    editable: true

  # Remote Prometheus on ECS
  - name: Prometheus-ECS
    type: prometheus
    access: proxy
    url: http://<PROM_NLB_DNS>:9090
    editable: true
```

Replace `<PROM_NLB_DNS>` with the NLB hostname from:

```bash
cd ~/cs6650/final_project/terraform && terraform output -raw prometheus_url
# This prints: http://cs6650-final-prom-nlb-XXXXX.elb.us-west-2.amazonaws.com:9090
# Use just the hostname part (without http:// and :9090) for the url field,
# OR use the full URL as-is since datasources-combined.yml already includes the port
```

### 3.2 Start Local Grafana Stack

```bash
cd ~/cs6650/final_project
docker compose -f docker-compose.grafana.yml up -d
```

This starts two containers:
- `prometheus-local` on port 9090
- `grafana` on port 3000

### 3.3 Verify Prometheus Targets

Open http://localhost:9090/targets in your browser.

You should see:
- **cart-api** target pointing to your ALB -- status should be **UP**
- **ai-agent** target pointing to `host.docker.internal:8083` -- status will be **DOWN** until you run the AI agent (Step 5/6)

If the cart-api target shows DOWN, double-check the ALB DNS in `prometheus-local-agent.yml` and restart:

```bash
docker compose -f docker-compose.grafana.yml restart prometheus-local
```

### 3.4 Open Grafana

Open http://localhost:3000 in your browser.

- **Username:** `admin`
- **Password:** `admin`
- Skip the password change prompt (or change it if you want)

The default home dashboard is **System Overview**, which should be available at:

```
http://localhost:3000/d/cs6650-overview
```

### 3.5 Import Dashboards

Dashboards are auto-provisioned from `monitoring/grafana/dashboards/` via the `dashboard-provider.yml` config. If they do not appear, you can manually import:

1. In Grafana, click the **+** icon in the sidebar, then **Import**
2. Click **Upload JSON file**
3. Select `monitoring/grafana/dashboards/system-overview.json`
4. Choose the **Prometheus** datasource
5. Click **Import**

### 3.6 Understanding the Dashboard Panels

The **System Overview** dashboard includes:

| Panel | What It Shows | What to Look For |
|-------|--------------|------------------|
| CPU Usage (%) | Simulated CPU from cart-api metrics | Should rise under load, trigger alerts above 80% |
| Memory (RSS + Heap) | Go runtime memory stats | Steady growth under load, should plateau |
| HTTP Request Rate | Requests per second to cart-api | Matches your Locust user count roughly |
| Latency Percentiles (p50/p95/p99) | Response time distribution | p99 above 500ms triggers alerts |
| Goroutines | Active Go goroutines | Correlates with concurrent connections |
| Kafka Publish Rate | Messages/sec published to metrics-topic | Should track request rate |
| Error Rate | HTTP 4xx/5xx percentage | Above 5% triggers alerts |

---

## Step 4: Run Load Tests

### 4.1 Install Locust

```bash
pip install locust
```

Or use the requirements file:

```bash
pip install -r ~/cs6650/final_project/loadtest/requirements.txt
```

### 4.2 Get Your ALB DNS

```bash
ALB_DNS=$(cd ~/cs6650/final_project/terraform && terraform output -raw alb_dns_name)
echo "Target: http://$ALB_DNS"
```

### 4.3 Load Test Profiles

There are 4 profiles, each designed for different experiments:

**Profile 1: SteadyUser -- Constant baseline load**

Good for baseline measurements. Each user sends a request every 200ms.

```bash
cd ~/cs6650/final_project/loadtest

# Light test (50 users, 3 minutes)
locust -f locustfile.py SteadyUser \
  --host=http://<ALB_DNS> \
  --users 50 --spawn-rate 10 --run-time 3m --headless

# Medium test (200 users, 5 minutes)
locust -f locustfile.py SteadyUser \
  --host=http://<ALB_DNS> \
  --users 200 --spawn-rate 20 --run-time 5m --headless

# Heavy test (500 users, 5 minutes)
locust -f locustfile.py SteadyUser \
  --host=http://<ALB_DNS> \
  --users 500 --spawn-rate 50 --run-time 5m --headless
```

**Profile 2: RampUser -- Gradual ramp-up**

Same request pattern as SteadyUser. Use `--spawn-rate` to control how fast users are added.

```bash
cd ~/cs6650/final_project/loadtest

# Ramp from 0 to 1000 users over ~5 minutes (spawn 3/sec)
locust -f locustfile.py RampUser \
  --host=http://<ALB_DNS> \
  --users 1000 --spawn-rate 3 --run-time 5m --headless
```

**Profile 3: SpikeUser -- Alternating bursts**

Alternates between calm (1-2s between requests) and burst (10-50ms) every 30 seconds.

```bash
cd ~/cs6650/final_project/loadtest

locust -f locustfile.py SpikeUser \
  --host=http://<ALB_DNS> \
  --users 500 --spawn-rate 50 --run-time 3m --headless
```

**Profile 4: StepUser -- Step function**

Constant per-user rate; combine with Locust step-load to increase users in discrete jumps.

```bash
cd ~/cs6650/final_project/loadtest

locust -f locustfile.py StepUser \
  --host=http://<ALB_DNS> \
  --users 800 --spawn-rate 200 --run-time 8m --headless
```

### 4.4 Using the Locust Web UI

For interactive testing, omit `--headless`:

```bash
cd ~/cs6650/final_project/loadtest
locust -f locustfile.py --host=http://<ALB_DNS>
```

Then open http://localhost:8089 in your browser. You can configure users, spawn rate, and watch real-time charts.

### 4.5 Reading Locust Output

In headless mode, Locust prints a summary table every 10 seconds:

```
Type     Name           # reqs    # fails  Avg   Min   Max   Median  req/s
POST     /cart/items     1523      0(0.0%)  24    5     312   18      70.2
```

Key columns:
- **# fails**: Should be 0 or very low. High failures mean the service is overloaded or crashing.
- **Avg / p99**: Average and 99th percentile latency in ms.
- **req/s**: Requests per second.

### 4.6 Expected Grafana Behavior During Load Tests

While running a load test, watch the Grafana dashboard:

- **Request Rate** panel should climb as Locust ramps up
- **CPU** should rise proportionally to load
- **Latency** p50 stays low, p99 may spike under heavy load
- **Kafka Publish Rate** should track the request rate
- If CPU exceeds 70% with static auto-scaling, AWS will start launching additional cart-api tasks (visible in ECS console)

---

## Step 5: Run the AI Monitor (Claude Code Approach)

This is our primary AI agent approach. The monitor polls DynamoDB for aggregated metrics from the analytics service. When it detects an alert condition (high CPU, high latency, etc.), it invokes the `claude` CLI to analyze the situation and optionally apply Terraform changes.

### 5.1 Prerequisites

- Claude Code CLI must be installed and authenticated: `claude --version`
- Terraform must be initialized: `cd terraform && terraform init`
- Infrastructure must be deployed and running (Step 2)
- Load tests should be running (Step 4) to generate metrics

### 5.2 Build the Monitor

```bash
cd ~/cs6650/final_project/services/monitor
go build -o ./monitor .
```

### 5.3 Run in Dry-Run Mode (Safe -- Recommended First)

Dry-run mode prints the prompt that would be sent to Claude Code but does NOT actually invoke it.

```bash
cd ~/cs6650/final_project
./scripts/run-monitor.sh --dry-run
```

Or run the binary directly:

```bash
cd ~/cs6650/final_project/services/monitor
./monitor \
  --table=cs6650-final-metrics \
  --terraform-dir=~/cs6650/final_project/terraform \
  --cooldown=120 \
  --poll=10 \
  --dry-run
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--table` | `cs6650-final-metrics` | DynamoDB table name |
| `--terraform-dir` | auto-detected | Path to terraform directory |
| `--cooldown` | `120` | Seconds between Claude Code invocations |
| `--poll` | `10` | Seconds between DynamoDB polls |
| `--dry-run` | `false` | Print prompt only, do not invoke Claude |

### 5.4 Run in Live Mode

Live mode actually invokes Claude Code with `--print --dangerously-skip-permissions`. Claude will read the metrics, check ECS status, check CloudWatch logs, and potentially modify `terraform.tfvars` and run `terraform apply`.

```bash
cd ~/cs6650/final_project
./scripts/run-monitor.sh
```

### 5.5 How to Read Monitor Output

The monitor logs to stdout. Example output:

```
2026/03/29 14:30:00 Monitor starting: table=cs6650-final-metrics poll=10s cooldown=120s dry_run=false terraform=/home/zongwang/cs6650/final_project/terraform
2026/03/29 14:30:00 Polling DynamoDB for new metrics...
2026/03/29 14:30:10 Metric: cpu=45.2% p99=24ms rate=70.1/s trend=stable instances=4
2026/03/29 14:30:20 Metric: cpu=72.3% p99=45ms rate=150.0/s trend=increasing instances=4
2026/03/29 14:30:20 Invoking Claude Code for alert: HIGH_CPU_INCREASING
2026/03/29 14:30:20 >>> Claude Code invoked >>>
... Claude's analysis and actions appear here ...
2026/03/29 14:30:45 <<< Claude Code finished (25.3s) <<<
```

### 5.6 Alert Conditions and Thresholds

The monitor evaluates these conditions on each new metric:

| Alert Type | Condition | Description |
|-----------|-----------|-------------|
| `HIGH_CPU_INCREASING` | CPU > 70% AND trend = "increasing" | CPU is rising and already high |
| `SUSTAINED_HIGH_CPU` | CPU > 80% for 3+ consecutive windows | Persistent overload |
| `SUSTAINED_LOW_CPU` | CPU < 30% for 3+ consecutive windows | Over-provisioned, should scale down |
| `HIGH_ERROR_RATE` | Error rate > 5% | Too many failures |
| `HIGH_LATENCY` | P99 latency > 500ms | Response times degraded |

When Claude Code is invoked in live mode, it will:
1. Check current ECS service status (`aws ecs describe-services`)
2. Check recent CloudWatch logs
3. Check DynamoDB metrics for recent trends
4. Decide whether to scale up, scale down, or take no action
5. If scaling is needed: edit `terraform.tfvars` (change `ai_desired_count` and `scaling_mode`), then run `terraform apply`

---

## Step 6: Run the AI Agent (Claude API Approach, Optional)

This is an alternative approach that uses the Claude API directly (via the Anthropic Go SDK) instead of the Claude Code CLI. It consumes from Kafka's `analytics-output-topic` instead of polling DynamoDB.

### 6.1 Set Up the API Key

```bash
cd ~/cs6650/final_project/services/ai-agent
cp .env.example .env
```

Edit `.env` and fill in your Anthropic API key:

```bash
ANTHROPIC_API_KEY=sk-ant-api03-your-actual-key-here
KAFKA_BROKERS=<KAFKA_NLB_DNS>:9092
TERRAFORM_DIR=/home/<your-username>/cs6650/final_project/terraform
DRY_RUN=true
COOLDOWN_SECONDS=60
PORT=8083
INSTANCE_ID=ai-agent-local
KAFKA_CONSUMER_TOPIC=analytics-output-topic
KAFKA_PRODUCER_TOPIC=infra-events-topic
CONSUMER_GROUP=ai-agent-group
```

Replace `<KAFKA_NLB_DNS>` with:

```bash
cd ~/cs6650/final_project/terraform && terraform output -raw kafka_nlb_dns_name
```

### 6.2 Run in Dry-Run Mode

```bash
cd ~/cs6650/final_project
./scripts/run-agent.sh --dry
```

This will:
- Load `.env` from `services/ai-agent/.env`
- Auto-detect Kafka NLB from Terraform output
- Build the agent binary
- Run in dry-run mode (logs what Claude would decide, but does not apply Terraform)

### 6.3 Run in Live Mode

```bash
cd ~/cs6650/final_project
./scripts/run-agent.sh --live
```

In live mode, the agent will actually modify `terraform.tfvars` and run `terraform apply` when Claude decides to scale.

### 6.4 How It Works

The agent loop:
1. Consumes aggregated metrics from `analytics-output-topic` (published every 10s by the analytics service)
2. Builds a prompt with current metrics + last 5 windows of history
3. Calls the Claude API (claude-sonnet-4-20250514) asking for a JSON scaling decision
4. If action needed and confidence >= 0.7: writes new `ai_desired_count` to `terraform.tfvars`
5. Runs `terraform apply -auto-approve`
6. Publishes the decision audit log to `infra-events-topic`

Safety guardrails:
- Max change per cycle: +/- 2 tasks
- Min cooldown: 60 seconds between applies
- Task count range: 1 to 8
- Dry-run mode available for testing

### 6.5 Check Agent Status

While the agent is running, you can check its status endpoint:

```bash
curl http://localhost:8083/status
```

---

## Step 7: SNS Email Alerts

The alert service publishes threshold-breach notifications to an SNS topic. You need to subscribe your email to receive them.

### 7.1 Get the SNS Topic ARN

```bash
aws sns list-topics --region us-west-2 --query "Topics[?contains(TopicArn, 'cs6650-final-alerts')].TopicArn" --output text
```

This returns something like: `arn:aws:sns:us-west-2:330214284546:cs6650-final-alerts`

### 7.2 Subscribe Your Email

```bash
aws sns subscribe \
  --topic-arn <SNS_TOPIC_ARN> \
  --protocol email \
  --notification-endpoint your.email@northeastern.edu \
  --region us-west-2
```

### 7.3 Confirm Subscription

Check your email inbox for a confirmation email from AWS SNS. Click the **Confirm subscription** link. You must confirm before you receive any alerts.

### 7.4 Test the Subscription

```bash
aws sns publish \
  --topic-arn <SNS_TOPIC_ARN> \
  --message "Test alert from cs6650-final project" \
  --subject "Test Alert" \
  --region us-west-2
```

You should receive an email within a few seconds.

### 7.5 When Do Alerts Fire?

The alert service evaluates every Kafka message from `metrics-topic` against these thresholds:
- **CPU > 80%** sustained for 30+ seconds
- **Error rate > 5%**
- **P99 latency > 500ms**

There is a 5-minute cooldown between duplicate alert types (so you will not get spammed).

---

## Step 8: Tear Down

### 8.1 Stop Local Services

```bash
cd ~/cs6650/final_project

# Stop Grafana + local Prometheus
docker compose -f docker-compose.grafana.yml down

# If you ran the full local stack at any point:
docker compose down
```

### 8.2 Stop the Monitor / Agent

If you have `run-monitor.sh` or `run-agent.sh` running, press `Ctrl+C` to stop them. They handle SIGINT gracefully.

### 8.3 Destroy AWS Infrastructure

```bash
cd ~/cs6650/final_project/terraform
terraform destroy -auto-approve
```

This removes everything: VPC, ALB, ECS cluster, all ECS services, ECR repos, DynamoDB table, SNS topic, NLBs, security groups, etc.

It takes about 3-5 minutes.

### 8.4 Clean Up ECR Login

Docker credentials for ECR are stored locally. To clean up:

```bash
docker logout <ACCOUNT_ID>.dkr.ecr.us-west-2.amazonaws.com
```

### 8.5 Verify Tear Down

```bash
# Should show no ECS clusters
aws ecs list-clusters --region us-west-2

# Should show no project-related load balancers
aws elbv2 describe-load-balancers --region us-west-2 --query "LoadBalancers[?contains(LoadBalancerName, 'cs6650')]"
```

---

## Troubleshooting

### Kafka NLB DNS Changed After Terraform Destroy/Apply

**Symptom:** Services start but cannot produce/consume Kafka messages. CloudWatch logs show connection timeouts to the old Kafka NLB DNS.

**Fix:** The Kafka NLB DNS is baked into every ECS task definition via Terraform. After `terraform apply`, the new DNS is automatically used in task definitions. You just need to force new deployments:

```bash
for svc in cart-api analytics alert; do
  aws ecs update-service --cluster cs6650-final-cluster --service "cs6650-final-$svc" --force-new-deployment --region us-west-2 --no-cli-pager > /dev/null
done
```

Also update `monitoring/prometheus/prometheus-ecs.yml` with the new ALB DNS, rebuild and push the Prometheus image, then force-deploy Prometheus too.

### Alert Service Not Consuming Messages

**Symptom:** Alert service is running but not firing any alerts during load tests.

**Possible causes:**
1. Kafka topics not created (check with the kafka-init task)
2. Wrong Kafka NLB DNS in the alert task definition
3. Consumer group offset issue -- the consumer may have joined before topics existed

**Fix:** Force a new deployment of the alert service:

```bash
aws ecs update-service --cluster cs6650-final-cluster --service cs6650-final-alert --force-new-deployment --region us-west-2 --no-cli-pager > /dev/null
```

Check CloudWatch logs:

```bash
aws logs filter-log-events \
  --log-group-name "/ecs/cs6650-final/alert" \
  --start-time $(($(date +%s) - 300))000 \
  --limit 20 --region us-west-2 \
  --query "events[].message" --output text --no-cli-pager
```

### Grafana Shows No Data

**Symptom:** Dashboard panels say "No data" or show flat lines.

**Checks:**

1. Open http://localhost:9090/targets -- are the targets UP?
2. If the cart-api target is DOWN, the ALB DNS in `prometheus-local-agent.yml` is wrong. Update it and restart:
   ```bash
   docker compose -f docker-compose.grafana.yml restart prometheus-local
   ```
3. If the Prometheus-ECS datasource shows errors in Grafana, the Prometheus NLB DNS in `datasources-combined.yml` is wrong. Update it and restart:
   ```bash
   docker compose -f docker-compose.grafana.yml restart grafana
   ```
4. Make sure you are sending traffic (run a Locust test) -- there are no metrics if there are no requests.

### CPU at 100% in Grafana

**Symptom:** The CPU metric shows 100% even with light load.

**Cause:** This is likely an old cart-api Docker image that had a CPU-intensive simulation. Rebuild and push a fresh image:

```bash
docker build -t cart-api ~/cs6650/final_project/services/cart-api
docker tag cart-api $ECR_BASE/cs6650-final/cart-api:latest
docker push $ECR_BASE/cs6650-final/cart-api:latest
aws ecs update-service --cluster cs6650-final-cluster --service cs6650-final-cart-api --force-new-deployment --region us-west-2 --no-cli-pager > /dev/null
```

### ECR Login Expired

**Symptom:** `docker push` fails with "no basic auth credentials" or "denied".

**Fix:** Re-run the ECR login (it expires after 12 hours):

```bash
aws ecr get-login-password --region us-west-2 | docker login --username AWS --password-stdin $(aws sts get-caller-identity --query Account --output text).dkr.ecr.us-west-2.amazonaws.com
```

### Lab Account Permission Denied

**Symptom:** Terraform or AWS CLI returns "AccessDenied" or "not authorized".

**Common causes:**
- Trying to create IAM roles -- we cannot do this; everything uses `LabRole`
- Trying to use SES -- not available; we use SNS instead
- Trying to use Cloud Map / Service Discovery -- not available; we use NLB instead
- Session token expired -- lab accounts have temporary credentials that expire. Go to your lab console and get fresh credentials, then re-run `aws configure`.

### Terraform State Lock

**Symptom:** `terraform apply` fails with "Error acquiring the state lock".

**Fix:**

```bash
cd ~/cs6650/final_project/terraform
terraform force-unlock <LOCK_ID>
```

The lock ID is shown in the error message.

### Monitor Cannot Find Terraform Directory

**Symptom:** `run-monitor.sh` fails with "Could not detect Kafka NLB".

**Fix:** Make sure Terraform is applied and the state exists:

```bash
cd ~/cs6650/final_project/terraform
terraform init
terraform output
```

If outputs are empty, you need to run `terraform apply` first (Step 2).

### ECS Tasks Keep Restarting

**Symptom:** Tasks show as RUNNING briefly then stop, cycling repeatedly.

**Check CloudWatch logs for the failing service:**

```bash
# Replace <service-name> with cart-api, analytics, alert, kafka, or prometheus
aws logs filter-log-events \
  --log-group-name "/ecs/cs6650-final/<service-name>" \
  --start-time $(($(date +%s) - 600))000 \
  --limit 30 --region us-west-2 \
  --query "events[].message" --output text --no-cli-pager
```

Common causes:
- Kafka not ready yet (services depend on Kafka being accessible)
- Wrong environment variables in the task definition
- Image pull failure (ECR login issue or image does not exist)

---

## Quick Reference

### Important URLs

| Resource | URL |
|----------|-----|
| ALB (cart-api) | `http://<ALB_DNS>/` |
| Health Check | `http://<ALB_DNS>/health` |
| Prometheus (ECS) | `http://<PROM_NLB_DNS>:9090` |
| Local Prometheus | http://localhost:9090 |
| Local Prometheus Targets | http://localhost:9090/targets |
| Grafana | http://localhost:3000 (admin/admin) |
| System Overview Dashboard | http://localhost:3000/d/cs6650-overview |
| Locust Web UI | http://localhost:8089 (when run without --headless) |
| AI Agent Status | http://localhost:8083/status |

### Get Current DNS Values

```bash
cd ~/cs6650/final_project/terraform
terraform output -raw alb_dns_name          # ALB for cart-api
terraform output -raw kafka_nlb_dns_name    # Kafka NLB (internal)
terraform output -raw prometheus_url        # Prometheus NLB (public)
```

### Common Command Sequences

**Full deploy from scratch:**

```bash
cd ~/cs6650/final_project/terraform && terraform init && terraform apply -auto-approve
cd ~/cs6650/final_project && ./scripts/deploy.sh --skip-infra
```

**Redeploy after code changes (no infra change):**

```bash
cd ~/cs6650/final_project && ./scripts/deploy.sh --skip-infra
```

**Quick load test:**

```bash
ALB_DNS=$(cd ~/cs6650/final_project/terraform && terraform output -raw alb_dns_name)
cd ~/cs6650/final_project/loadtest && locust -f locustfile.py SteadyUser --host=http://$ALB_DNS --users 50 --spawn-rate 10 --run-time 3m --headless
```

**Start monitoring:**

```bash
cd ~/cs6650/final_project && docker compose -f docker-compose.grafana.yml up -d
```

**Stop everything:**

```bash
cd ~/cs6650/final_project
docker compose -f docker-compose.grafana.yml down
cd terraform && terraform destroy -auto-approve
```

### Makefile Targets

Run from the project root (`~/cs6650/final_project`):

| Target | Command | Description |
|--------|---------|-------------|
| `make help` | -- | Show all available targets |
| `make up` | `docker compose up -d --build` | Start full local stack |
| `make down` | `docker compose down` | Stop full local stack |
| `make kafka-up` | `docker compose -f docker-compose.kafka.yml up -d` | Start Kafka only (local) |
| `make kafka-down` | `docker compose -f docker-compose.kafka.yml down` | Stop Kafka (local) |
| `make topics` | `bash scripts/setup-kafka-topics.sh` | Create Kafka topics (local) |
| `make logs` | `docker compose logs -f` | Tail all service logs |
| `make build` | `go build` cart-api | Build cart-api binary locally |
| `make test` | `go test` | Run Go tests |
| `make locust` | Locust web UI | Open Locust at localhost:8089 |
| `make locust-steady` | SteadyUser, 200 users, 2 min | Headless steady load test |
| `make locust-ramp` | RampUser, 1000 users, 5 min | Headless ramp load test |
| `make locust-spike` | SpikeUser, 500 users, 3 min | Headless spike load test |
| `make tf-init` | `terraform init` | Initialize Terraform |
| `make tf-plan` | `terraform plan` | Preview Terraform changes |
| `make tf-apply` | `terraform apply -auto-approve` | Apply Terraform (static mode) |
| `make tf-apply-ai` | `terraform apply -auto-approve -var=scaling_mode=ai` | Apply with AI scaling |
| `make tf-destroy` | `terraform destroy -auto-approve` | Destroy all AWS resources |

### Credentials

| Service | Username | Password |
|---------|----------|----------|
| Grafana | `admin` | `admin` |
| AWS ECR | `AWS` | (from `aws ecr get-login-password`) |

### Key File Locations

| File | Purpose |
|------|---------|
| `terraform/terraform.tfvars` | Scaling parameters (the AI agent modifies this) |
| `terraform/main.tf` | All AWS infrastructure |
| `monitoring/prometheus/prometheus-ecs.yml` | ECS Prometheus scrape config (has ALB DNS) |
| `monitoring/prometheus/prometheus-local-agent.yml` | Local Prometheus scrape config (has ALB DNS) |
| `monitoring/grafana/datasources-combined.yml` | Grafana datasource config (has Prom NLB DNS) |
| `services/ai-agent/.env` | AI agent config (API key, Kafka DNS) |
| `scripts/deploy.sh` | Full deployment script |
| `scripts/run-monitor.sh` | Launch the DynamoDB-polling monitor |
| `scripts/run-agent.sh` | Launch the Kafka-consuming AI agent |
