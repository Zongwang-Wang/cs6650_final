# Implementation Plan — Real-Time Distributed Monitoring + AI DevOps Agent

## Architecture Overview

```
                        ┌─────────────────────────────────────────────────────┐
                        │                    AWS ECS Cluster                   │
                        │                                                     │
  Locust ──► ┌──────────────────┐    ┌───────────────────────────────────┐    │
  (load)     │  Shopping Cart   │    │        Kafka (KRaft, 3 brokers)  │    │
             │  API (Go, N tasks│───►│  metrics-topic (partitioned)     │    │
             │  + Prometheus    │    │  infra-events-topic              │    │
             │    /metrics)     │    └──────┬──────────────┬────────────┘    │
             └──────────────────┘           │              │                  │
                                           │              │                  │
                        ┌──────────────────┘              └───────────┐      │
                        ▼                                             ▼      │
              ┌──────────────────┐                          ┌─────────────┐  │
              │ Analytics Service│                          │Alert Service│  │
              │ (Go consumer)   │                          │(Go consumer)│  │
              │ - rolling window│                          │- threshold  │  │
              │ - trend export  │                          │- SES email  │  │
              │ - Prometheus    │                          └─────────────┘  │
              │   /metrics      │                                           │
              └───────┬─────────┘                                           │
                      │ (Kafka: analytics-output-topic)                     │
                      ▼                                                     │
              ┌──────────────────┐     ┌─────────────┐                      │
              │  AI DevOps Agent │────►│  Terraform   │──► ECS/Kafka scaling│
              │  (Go consumer)   │     │  Apply       │                     │
              │  - Claude API    │     └─────────────┘                      │
              │  - publishes to  │                                          │
              │    infra-events  │                                          │
              └──────────────────┘                                          │
                                                                            │
              ┌──────────────────────────────────┐                          │
              │  Prometheus  ◄── scrapes /metrics │                          │
              │       │                          │                          │
              │       ▼                          │                          │
              │  Grafana Dashboards              │                          │
              │  - CPU / request metrics         │                          │
              │  - Kafka consumer lag            │                          │
              │  - AI agent decisions (audit)    │                          │
              └──────────────────────────────────┘                          │
              └─────────────────────────────────────────────────────────────┘
```

## Project Directory Structure

```
final_project/
├── api.yaml                          # OpenAPI spec (existing)
├── IMPLEMENTATION_PLAN.md            # This file
├── docker-compose.yml                # Local dev: all services
├── docker-compose.kafka.yml          # Kafka-only (3 brokers, KRaft)
├── Makefile                          # Common commands
│
├── services/
│   ├── cart-api/                     # Shopping Cart API (Go) — the "producer"
│   │   ├── main.go
│   │   ├── handler.go                # POST /cart/items endpoint
│   │   ├── kafka.go                  # Kafka producer (metrics-topic)
│   │   ├── metrics.go                # Prometheus instrumentation
│   │   ├── Dockerfile
│   │   └── go.mod
│   │
│   ├── analytics/                    # Analytics Service (Go consumer)
│   │   ├── main.go
│   │   ├── consumer.go               # Kafka consumer (metrics-topic)
│   │   ├── aggregator.go             # Rolling window CPU avg, trend
│   │   ├── producer.go               # Kafka producer (analytics-output-topic)
│   │   ├── metrics.go                # Prometheus metrics export
│   │   ├── Dockerfile
│   │   └── go.mod
│   │
│   ├── alert/                        # Alert Service (Go consumer)
│   │   ├── main.go
│   │   ├── consumer.go               # Kafka consumer (metrics-topic)
│   │   ├── evaluator.go              # Threshold checks
│   │   ├── notifier.go               # SES/SendGrid email
│   │   ├── Dockerfile
│   │   └── go.mod
│   │
│   └── ai-agent/                     # AI DevOps Agent (Go)
│       ├── main.go
│       ├── consumer.go               # Kafka consumer (analytics-output-topic)
│       ├── claude.go                  # Claude API client
│       ├── terraform.go              # Generate & apply Terraform changes
│       ├── producer.go               # Kafka producer (infra-events-topic)
│       ├── Dockerfile
│       └── go.mod
│
├── terraform/                        # Terraform configs (managed by AI agent)
│   ├── main.tf                       # ECS cluster, Kafka, networking
│   ├── variables.tf                  # Tunable knobs (task_count, partitions)
│   ├── outputs.tf
│   └── terraform.tfvars              # Current state (AI agent writes here)
│
├── monitoring/
│   ├── prometheus/
│   │   └── prometheus.yml            # Scrape configs
│   └── grafana/
│       ├── datasources.yml
│       └── dashboards/
│           ├── system-overview.json
│           └── ai-agent-audit.json
│
├── loadtest/
│   └── locustfile.py                 # Locust load test against cart-api
│
└── scripts/
    ├── setup-kafka-topics.sh         # Create topics with partitions
    └── run-experiments.sh            # Automated experiment runner
```

## Kafka Topics

| Topic | Partitions | Producers | Consumers | Purpose |
|---|---|---|---|---|
| `metrics-topic` | 6 | cart-api (N instances) | analytics, alert | Raw per-request metrics (latency, status, simulated CPU) |
| `analytics-output-topic` | 3 | analytics | ai-agent | Aggregated trends (avg CPU, p99 latency, request rate) |
| `infra-events-topic` | 1 | ai-agent | Grafana (via Prometheus exporter) | Audit log of every AI scaling decision |

## Service Details

### 1. Shopping Cart API (`cart-api`) — The Producer

**Language:** Go
**Single endpoint for demo:** `POST /cart/items`

```
POST /cart/items
{
  "product_id": 123,
  "quantity": 2,
  "customer_id": 456
}
→ 201 { "cart_id": "uuid", "items_count": 2 }
```

**Responsibilities:**
- Handle HTTP requests (net/http or chi router)
- Store cart state in-memory (map[string][]CartItem) — no DB needed for demo
- On every request, publish a metric event to `metrics-topic`:
  ```json
  {
    "timestamp": "2026-03-28T12:00:00Z",
    "service": "cart-api",
    "instance_id": "cart-api-1",
    "endpoint": "/cart/items",
    "method": "POST",
    "status_code": 201,
    "latency_ms": 12,
    "cpu_percent": 45.2
  }
  ```
- Expose `/metrics` for Prometheus (request count, latency histogram, active goroutines)
- **Simulated CPU:** use a goroutine that does busy-work proportional to request rate, or simply report `runtime` CPU stats. Under Locust load, real CPU usage will naturally climb.

**Key Go packages:** `github.com/segmentio/kafka-go`, `github.com/prometheus/client_golang`, `github.com/google/uuid`

### 2. Analytics Service (`analytics`)

**Consumes:** `metrics-topic` (consumer group: `analytics-group`)
**Produces:** `analytics-output-topic`

**Logic:**
- Maintains a 60-second rolling window of metric events
- Every 10 seconds, computes and publishes:
  ```json
  {
    "timestamp": "2026-03-28T12:01:00Z",
    "window_seconds": 60,
    "avg_cpu_percent": 67.3,
    "max_cpu_percent": 89.1,
    "avg_latency_ms": 45,
    "p99_latency_ms": 120,
    "request_rate_per_sec": 350,
    "error_rate_percent": 0.5,
    "trend": "increasing",
    "instance_count": 3
  }
  ```
- Exports these as Prometheus gauges for Grafana

### 3. Alert Service (`alert`)

**Consumes:** `metrics-topic` (consumer group: `alert-group`)

**Logic:**
- Evaluates per-event thresholds:
  - CPU > 80% for 30+ seconds → alert
  - Error rate > 5% → alert
  - Latency p99 > 500ms → alert
- Sends email via AWS SES (or logs to stdout for demo)
- Deduplication: cooldown period per alert type (5 minutes)

### 4. AI DevOps Agent (`ai-agent`)

**Consumes:** `analytics-output-topic` (consumer group: `ai-agent-group`)
**Produces:** `infra-events-topic`

**Core loop:**
```
1. Consume aggregated metric from analytics-output-topic
2. Build prompt with current metrics + recent history (last 5 windows)
3. Call Claude API with structured output request
4. Parse response → Terraform variable changes
5. Write terraform.tfvars
6. Run `terraform plan` → `terraform apply -auto-approve`
7. Publish decision + result to infra-events-topic
```

**Claude API prompt structure:**
```
You are an infrastructure scaling advisor. Given the following metrics,
decide if any scaling action is needed.

Current metrics:
- avg_cpu: 78%, trend: increasing
- request_rate: 500/s, trend: stable
- p99_latency: 200ms
- current ECS task count: 3
- current Kafka partitions: 6

Recent history (last 5 windows):
[... array of previous metrics ...]

Respond with JSON:
{
  "action": "scale_up" | "scale_down" | "no_action",
  "reasoning": "string",
  "changes": {
    "cart_api_task_count": 5,      // or null if no change
    "kafka_partitions": 9           // or null if no change
  },
  "confidence": 0.85
}

Rules:
- Scale up if avg CPU > 70% with increasing trend
- Scale down if avg CPU < 30% for 3+ consecutive windows
- Never scale below 1 task or above 20 tasks
- Never decrease Kafka partitions (Kafka doesn't support this)
- Prefer conservative changes (increment by 1-2 tasks)
```

**Safety guardrails:**
- Confidence threshold: only apply if confidence >= 0.7
- Max change per cycle: ±2 tasks
- Cooldown: minimum 60 seconds between applies
- Dry-run mode: log what would change without applying (for testing)

### 5. Monitoring Stack

**Prometheus** scrapes:
- `cart-api:8080/metrics`
- `analytics:8081/metrics`
- `alert:8082/metrics`
- `ai-agent:8083/metrics`

**Grafana dashboards:**

1. **System Overview** — request rate, latency p50/p99, CPU per instance, Kafka consumer lag
2. **AI Agent Audit** — timeline of scaling decisions, reasoning text, before/after task counts, confidence scores

## Docker Compose (Local Dev)

```yaml
# Services: kafka-1, kafka-2, kafka-3, cart-api, analytics, alert, ai-agent, prometheus, grafana
# All in one network, Kafka in KRaft mode (no ZooKeeper)
```

Kafka KRaft setup uses `KAFKA_CFG_PROCESS_ROLES=broker,controller` with 3 nodes forming a quorum. Uses Bitnami Kafka image for simplicity.

## Terraform (AWS ECS)

Manages:
- ECS cluster + service definitions (task count is the tunable knob)
- Kafka on MSK or self-hosted on ECS
- ALB for cart-api
- Security groups, VPC, subnets
- CloudWatch log groups

The AI agent modifies `terraform.tfvars` to change:
- `cart_api_desired_count` (ECS task count)
- Other tunable parameters

## Load Testing (Locust)

```python
# locustfile.py
# POST /cart/items with random product_id, quantity, customer_id
# Ramp from 10 to 1000 users over 5 minutes
# Sustained load at 1000 users for 10 minutes
# Step load pattern for experiment 5 (scaling accuracy)
```

Profiles:
- **Steady load**: 200 users, 5 req/s each → ~1000 req/s
- **Ramp up**: 10 → 1000 users over 5 min
- **Spike**: 200 → 2000 users instantly, hold 2 min, drop back
- **Step function**: 200 → 500 → 800 → 1000, 2 min each step

## Implementation Phases

### Phase 1: Kafka Backbone + Cart API (Days 1-2)

**Files to create:**
1. `docker-compose.kafka.yml` — 3-broker KRaft Kafka cluster
2. `scripts/setup-kafka-topics.sh` — create all 3 topics
3. `services/cart-api/` — full service with Kafka producer
4. `loadtest/locustfile.py` — basic load test

**Milestone:** Locust → cart-api → messages visible in metrics-topic

### Phase 2: Consumers + Observability (Days 3-4)

**Files to create:**
1. `services/analytics/` — full service
2. `services/alert/` — full service
3. `monitoring/prometheus/prometheus.yml`
4. `monitoring/grafana/` — datasource + dashboard configs
5. `docker-compose.yml` — full stack

**Milestone:** Grafana shows live CPU/latency charts; alerts fire on high load

### Phase 3: AI DevOps Agent (Days 5-6)

**Files to create:**
1. `services/ai-agent/` — full service
2. `terraform/` — base configs
3. `monitoring/grafana/dashboards/ai-agent-audit.json`

**Milestone:** Agent reads metrics, calls Claude, generates Terraform, publishes decisions

### Phase 4: Experiments + Polish (Days 7-8)

1. Run all 7 experiments, collect data
2. Tune agent prompt and guardrails
3. Add human-approval mode for large changes
4. Finalize dashboards
5. Write report

## Key Design Decisions

| Decision | Choice | Why |
|---|---|---|
| Single endpoint | `POST /cart/items` | Simplest demo that generates meaningful load |
| In-memory cart state | No database | Reduces infra complexity; focus is on Kafka pipeline |
| kafka-go library | segmentio/kafka-go | Pure Go, no CGO, good consumer group support |
| Simulated + real CPU | Hybrid | Real Go runtime stats under load + optional synthetic inflation |
| Terraform for scaling | Not AWS SDK directly | Declarative, auditable, matches project description |
| 60s rolling window | Not per-event | Smooths noise, gives agent stable signal |
| Cooldown between applies | 60 seconds | Prevents oscillation, lets changes take effect |

## Go Module Dependencies

All services share these core deps:
```
github.com/segmentio/kafka-go    # Kafka client
github.com/prometheus/client_golang  # Prometheus metrics
github.com/google/uuid           # UUIDs
```

AI agent additionally:
```
github.com/anthropics/anthropic-sdk-go  # Claude API
github.com/hashicorp/terraform-exec     # Terraform programmatic execution
```

## Experiment Execution Plan

| # | Experiment | Method | Key Metrics |
|---|---|---|---|
| 1 | Producer scalability | Scale cart-api 1→20, Locust constant load | Kafka throughput (msg/s), publish latency |
| 2 | Fault tolerance | `docker stop kafka-1`, observe | KRaft re-election time, message loss |
| 3 | Consumer scalability | Scale analytics 1→5 | Consumer lag, processing latency |
| 4 | Alert latency | Spike CPU > 80%, measure | End-to-end: metric → alert email time |
| 5 | Scaling accuracy | Step/ramp/spike Locust profiles | Decision latency, correct scaling rate |
| 6 | Closed-loop stability | 30-min sustained dynamic load | Convergence time, overshoot/undershoot |
| 7 | Agent failure isolation | Kill ai-agent mid-cycle | System continues? Recovery time? Offset resume? |
