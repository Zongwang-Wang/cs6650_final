# CS6650 Final Project — Status Report
**Last updated:** 2026-03-29
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
│   │   ├── handler.go                    #   POST /cart/items, GET /health
│   │   ├── kafka.go                      #   Kafka producer → metrics-topic
│   │   ├── metrics.go                    #   Prometheus: req count, duration, kafka publishes
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
- **ALB**: `cs6650-final-alb-1284626161.us-west-2.elb.amazonaws.com`
- **Kafka NLB** (internal): `cs6650-final-kafka-nlb-802d4f89e912ebcd.elb.us-west-2.amazonaws.com:9092`
- **Prometheus NLB** (public): `cs6650-final-prom-nlb-2d91161522d1aa47.elb.us-west-2.amazonaws.com:9090`
- **ECR repos**: cs6650-final/{cart-api, analytics, alert, prometheus}
- **DynamoDB table**: cs6650-final-metrics (96 items stored so far)
- **SNS topic**: cs6650-final-alerts → wang.zongw@northeastern.edu
- **IAM**: Uses pre-existing LabRole (lab account can't create custom roles)

### Auto-Scaling (Static Mode — Active)
- Target: cart-api ECS service
- Policy: CPU target tracking at 70%
- Min: 1, Max: 8, Desired: 4
- Scale-in cooldown: 300s, Scale-out cooldown: 300s

### Grafana Dashboard
- **URL**: http://localhost:3000/d/cs6650-overview (admin/admin)
- Panels: CPU usage, memory (RSS + heap), HTTP request rate, latency percentiles (p50/p95/p99), goroutines, Kafka publish rate, error rate, stats

### Load Testing (Locust)
- 4 profiles: SteadyUser, RampUser, SpikeUser, StepUser
- Light test completed: 50 users, 3 min, ~70 req/s, 0 failures, avg 24ms latency

### Known Lab Account Limitations
- Cannot create IAM roles (using LabRole instead)
- Cannot use Cloud Map / Service Discovery (using NLB for Kafka)
- Cannot use SES (using SNS for email alerts)
- Prometheus `ecs_sd_configs` not available in v2.54 (using static ALB target)

## TODOs for Tomorrow (Week 3 — AI Agent)

### Priority 1: AI DevOps Agent Deployment
- [ ] Add ai-agent ECR repo to Terraform
- [ ] Add ai-agent ECS task definition + service to Terraform
- [ ] Set ANTHROPIC_API_KEY as environment variable (or Secrets Manager)
- [ ] Build and push ai-agent Docker image
- [ ] Deploy and verify it consumes from analytics-output-topic
- [ ] Test Claude API integration (dry-run mode first)
- [ ] Test actual Terraform apply (change scaling_mode to "ai")
- [ ] Verify infra-events-topic receives decision audit logs

### Priority 2: Grafana AI Agent Audit Dashboard
- [ ] Create ai-agent-audit.json dashboard
- [ ] Panels: decision timeline, reasoning text, before/after task counts, confidence scores
- [ ] Connect infra-events-topic data (may need a Kafka → Prometheus exporter)

### Priority 3: Experiments
- [ ] **Experiment 5** — Scaling Decision Accuracy: inject step/ramp/spike loads, measure decision latency and correct scaling rate
- [ ] **Experiment 6** — Closed-Loop Stability: 30-min sustained dynamic load, measure convergence/oscillation
- [ ] **Experiment 7** — Agent Failure Isolation: kill ai-agent mid-cycle, verify system continues
- [ ] **Static vs AI comparison**: run same Locust profile under both scaling_mode="static" and "ai", compare Grafana metrics

### Priority 4: Polish
- [ ] Human-approval guardrails for large Terraform changes (>2 task delta)
- [ ] Tune Claude prompt for better scaling decisions
- [ ] Add more Prometheus metrics for analytics/alert services (currently only cart-api scraped via ALB)
- [ ] Final report and demo preparation
- [ ] Clean up Terraform state and documentation

## Quick Start (for tomorrow)

```bash
# 1. Re-deploy infrastructure
cd terraform && terraform init && terraform apply -auto-approve

# 2. Login to ECR
aws ecr get-login-password --region us-west-2 | docker login --username AWS --password-stdin 330214284546.dkr.ecr.us-west-2.amazonaws.com

# 3. Build and push all images
./scripts/deploy.sh --skip-infra

# 4. Start local Grafana
export PROMETHEUS_URL=$(cd terraform && terraform output -raw prometheus_url)
docker compose -f docker-compose.grafana.yml up -d

# 5. Open dashboard
# http://localhost:3000/d/cs6650-overview (admin/admin)

# 6. Run load test
cd loadtest && locust -f locustfile.py SteadyUser --host=http://$(cd ../terraform && terraform output -raw alb_dns_name) --users 50 --spawn-rate 10 --run-time 3m --headless
```
