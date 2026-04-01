# CS6650 Final Project — Status Report

**Last updated:** 2026-04-01
**Team:** Zongwang Wang, Dylan Pan, Lucas Chen, Ellis Guo

## Project Structure

```
final_project/
├── api.yaml                              # OpenAPI spec (reference)
├── IMPLEMENTATION_PLAN.md                # Full architecture & plan
├── PROJECT_STATUS.md                     # This file
├── Makefile                              # Common commands
├── docker-compose.yml                    # Full local stack (Kafka + all services)
├── docker-compose.kafka.yml              # Kafka-only (3-broker KRaft)
├── docker-compose.grafana.yml            # Local Grafana → remote Prometheus
│
├── services/
│   ├── cart-api/                          # Shopping Cart API (Go) — Producer
│   │   ├── main.go                       #   Entry point, HTTP server, graceful shutdown
│   │   ├── handler.go                    #   POST /cart/items (CPU burn, configurable via CPU_BURN_ITERATIONS)
│   │   ├── kafka.go                      #   Kafka producer + multi-source CPU tracker (cgroup / Getrusage)
│   │   ├── ecsmetadata.go                #   ECS Task Metadata Endpoint v4: container CPU% + memory RSS
│   │   ├── metrics.go                    #   Prometheus: req count, duration, kafka publishes, cpu%, memory
│   │   ├── Dockerfile                    #   Multi-stage (golang:1.22 → alpine:3.19)
│   │   └── go.mod
│   │
│   ├── analytics/                        # Analytics Service (Go) — Consumer
│   │   ├── main.go                       #   Entry point, wires consumer + aggregator + dynamo
│   │   ├── consumer.go                   #   Kafka consumer (metrics-topic, analytics-group)
│   │   ├── aggregator.go                 #   60s rolling window, computes avg CPU/latency/p99/trend
│   │   ├── producer.go                   #   Kafka producer → analytics-output-topic
│   │   ├── dynamo.go                     #   DynamoDB writer (persists every aggregation)
│   │   ├── metrics.go                    #   Prometheus: consumed, aggregations, window size
│   │   ├── Dockerfile
│   │   └── go.mod
│   │
│   ├── alert/                            # Alert Service (Go) — Consumer
│   │   ├── main.go                       #   Entry point, wires consumer + evaluator + notifier
│   │   ├── consumer.go                   #   Kafka consumer (metrics-topic, alert-group)
│   │   ├── evaluator.go                  #   Threshold checks: CPU>80%, errors>5%, p99>500ms
│   │   ├── notifier.go                   #   SNS email notifications + stdout JSON logging
│   │   ├── metrics.go                    #   Prometheus: consumed, alerts fired, active alerts
│   │   ├── Dockerfile
│   │   └── go.mod
│   │
│   └── ai-agent/                         # AI DevOps Agent (Go) — NOT DEPLOYED YET
│       ├── main.go                       #   Entry point, consumer + Claude API + Terraform
│       ├── consumer.go                   #   Kafka consumer (analytics-output-topic)
│       ├── claude.go                     #   Claude API client (raw HTTP, claude-sonnet-4-20250514)
│       ├── terraform.go                  #   Reads/writes terraform.tfvars, runs tf apply
│       ├── producer.go                   #   Kafka producer → infra-events-topic
│       ├── metrics.go                    #   Prometheus: decisions, applies, task count gauge
│       ├── Dockerfile                    #   Includes terraform binary
│       └── go.mod
│
├── terraform/                            # AWS Infrastructure
│   ├── main.tf                           #   VPC, ALB, ECS, Kafka(NLB), ECR, DynamoDB, SNS, auto-scaling
│   ├── variables.tf                      #   All variables (scaling_mode, counts, cooldowns)
│   ├── outputs.tf                        #   ALB DNS, ECR URLs, Prometheus URL
│   └── terraform.tfvars                  #   Current values (static mode, desired=4, max=8)
│
├── monitoring/
│   ├── prometheus/
│   │   ├── Dockerfile                    #   prom/prometheus:v2.54.0 + config baked in
│   │   ├── prometheus-ecs.yml            #   Scrape config for ECS (static targets via ALB)
│   │   └── prometheus.yml                #   Local dev scrape config
│   └── grafana/
│       ├── datasources.yml               #   Local Prometheus datasource
│       ├── datasources-remote.yml        #   Remote Prometheus NLB datasource
│       ├── dashboard-provider.yml        #   Dashboard provisioning config
│       └── dashboards/
│           └── system-overview.json      #   CPU, memory, req rate, latency, goroutines, Kafka
│
├── loadtest/
│   ├── locustfile.py                     #   4 profiles: Steady, Ramp, Spike, Step
│   ├── requirements.txt                  #   locust>=2.20
│   └── results/                          #   CSV outputs from test runs
│
└── scripts/
    ├── deploy.sh                         #   Full deploy: tf apply → docker build → ECR push → ECS update
    └── setup-kafka-topics.sh             #   Create 3 Kafka topics (idempotent)
```

## Current Features (v1 — What's Working)

### Services on AWS ECS (Fargate)

| Service | ECS Tasks | Status | Port |
|---|---|---|---|
| **Kafka** (KRaft, single broker) | 1 | Running | 9092 (internal NLB) |
| **cart-api** (behind ALB) | 2-4 (auto-scaled) | Running | 8080 (ALB:80) |
| **analytics** | 1 | Running | 8081 |
| **alert** | 1 | Running | 8082 |
| **Prometheus** | 1 | Running | 9090 (public NLB) |

### Data Flow (verified end-to-end)

```
Locust → ALB → cart-api → Kafka(metrics-topic) → analytics → Kafka(analytics-output-topic)
                                                             → DynamoDB (persistence)
                                                → alert → SNS email (on threshold breach)
Prometheus scrapes cart-api via ALB → Grafana (local :3000)
```

### Key Infrastructure

- **VPC**: 10.0.0.0/16, 2 public subnets (us-west-2a, us-west-2b)
- **ALB**: DNS changes on every `terraform apply` — read with `terraform output -raw alb_dns_name`
- **Kafka NLB** (internal): DNS changes on every `terraform apply` — read with `terraform output -raw kafka_nlb_dns_name`
- **Prometheus NLB** (public): DNS changes on every `terraform apply` — read with `terraform output -raw prometheus_url`
- **ECR repos**: cs6650-final/{cart-api, analytics, alert, prometheus}
- **DynamoDB table**: cs6650-final-metrics
- **SNS topic**: cs6650-final-alerts → <wang.zongw@northeastern.edu>
- **IAM**: Uses pre-existing LabRole (lab account can't create custom roles)

> ⚠️ After every `terraform apply`, update the ALB/NLB DNS names in `monitoring/prometheus/prometheus-local-agent.yml` and `monitoring/grafana/datasources-combined.yml` before starting local Grafana.

### Auto-Scaling (Static Mode — Active)

- Target: cart-api ECS service
- Policy: CPU target tracking at 70%
- Min: 1, Max: 8, Desired: 4
- Scale-in cooldown: 300s, Scale-out cooldown: 300s

### Grafana Dashboard

- **URL**: <http://localhost:3000/d/cs6650-overview> (admin/admin)
- Panels: CPU usage (`cart_api_cpu_percent` + `analytics_avg_cpu_percent`), memory RSS (`cart_api_memory_bytes`), HTTP request rate, latency percentiles (p50/p95/p99), goroutines, Kafka publish rate, error rate, Prometheus target status

### Load Testing (Locust)

- 4 profiles: SteadyUser, RampUser, SpikeUser, StepUser
- Light test completed: 50 users, 3 min, ~70 req/s, 0 failures, avg 24ms latency

### Known Lab Account Limitations

- Cannot create IAM roles (using LabRole instead)
- Cannot use Cloud Map / Service Discovery (using NLB for Kafka)
- Cannot use SES (using SNS for email alerts)
- Prometheus `ecs_sd_configs` not available in v2.54 (using static ALB target)

## Remaining TODOs

### Experiments (not yet run)

- [ ] **Experiment 5** — Scaling Decision Accuracy: step/ramp/spike loads, measure decision latency and correct scaling rate
- [ ] **Experiment 6** — Closed-Loop Stability: 30-min sustained dynamic load, measure convergence/oscillation
- [ ] **Experiment 7** — Agent Failure Isolation: kill ai-agent mid-cycle, verify system continues
- [ ] **Static vs AI comparison**: run same Locust profile under both `scaling_mode="static"` and `"ai"`, compare Grafana metrics

### Known Limitations (accepted)

- Prometheus scrapes via ALB only — metrics are aggregated across all cart-api tasks, not per-task
- Single-broker Kafka on ECS — no fault tolerance; broker restart = full outage
- All ECS tasks share LabRole (no least-privilege isolation)
- ai-agent (ECS approach) not deployed; monitor (Claude Code approach) is the active AI path

## Quick Start

```bash
# 1. Re-deploy infrastructure
cd terraform && terraform init && terraform apply -auto-approve

# 2. Login to ECR (account ID changes each lab session — read dynamically)
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
aws ecr get-login-password --region us-west-2 \
  | docker login --username AWS --password-stdin \
    "${ACCOUNT_ID}.dkr.ecr.us-west-2.amazonaws.com"

# 3. Build and push all images + update DNS configs
./scripts/deploy.sh --skip-infra

# 4. Update monitoring config with new DNS (after each terraform apply)
ALB_DNS=$(cd terraform && terraform output -raw alb_dns_name)
PROM_URL=$(cd terraform && terraform output -raw prometheus_url)
# Edit monitoring/prometheus/prometheus-local-agent.yml  → replace ALB target
# Edit monitoring/grafana/datasources-combined.yml       → replace Prometheus-ECS url

# 5. Start local Grafana
docker compose -f docker-compose.grafana.yml up -d

# 6. Open dashboard
# http://localhost:3000/d/cs6650-overview (admin/admin)

# 7. Run load test
cd loadtest && locust -f locustfile.py RampUser \
  --host="http://${ALB_DNS}" \
  --users 100 --spawn-rate 10 --run-time 8m --headless
```
