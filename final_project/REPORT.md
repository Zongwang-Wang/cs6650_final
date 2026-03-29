# Real-Time Distributed Monitoring System with AI DevOps Agent

**Course:** CS6650 Building Scalable Distributed Systems
**Team:** Zongwang Wang, Dylan Pan, Lucas Chen, Ellis Guo
**Date:** March 29, 2026

---

## Table of Contents

1. [Project Overview](#1-project-overview)
2. [System Architecture](#2-system-architecture)
3. [Technology Choices & Justification](#3-technology-choices--justification)
4. [Microservice Design Details](#4-microservice-design-details)
5. [Infrastructure Design](#5-infrastructure-design)
6. [Monitoring & Observability](#6-monitoring--observability)
7. [AI DevOps Agent Design](#7-ai-devops-agent-design)
8. [Challenges & Solutions](#8-challenges--solutions)
9. [Experiments (Planned)](#9-experiments-planned)
10. [Lessons Learned](#10-lessons-learned)

---

## 1. Project Overview

This project implements a **real-time distributed monitoring and auto-scaling system** for a cloud-native shopping cart API running on AWS ECS Fargate. The system consists of five microservices connected by Apache Kafka, with an AI-powered DevOps agent that autonomously analyzes system metrics and makes infrastructure scaling decisions via Terraform.

The core innovation is a **closed-loop feedback system**: the cart-api produces metrics under load, consumers aggregate and evaluate those metrics, and an AI agent powered by Claude decides whether to scale the infrastructure up or down -- then applies those changes automatically through Terraform, closing the loop.

### Goals

- Demonstrate distributed systems concepts: message queues, consumer groups, partitioned parallelism, fault tolerance
- Build a production-grade monitoring pipeline with Prometheus and Grafana
- Implement two auto-scaling strategies (static CPU target-tracking vs. AI-driven) and compare their effectiveness
- Explore the frontier of AI-driven infrastructure management with safety guardrails

### Current Status

All five microservices have been built, containerized, and deployed to AWS ECS Fargate. The end-to-end data flow has been verified: Locust generates load against the cart-api through an ALB, metrics flow through Kafka to the analytics and alert services, aggregated data is persisted to DynamoDB, and alerts are delivered via SNS email. The AI agent service is built and ready for deployment; the Claude Code monitor approach has been implemented as an alternative local agent.

---

## 2. System Architecture

### Architecture Diagram

```
                          AWS ECS Fargate Cluster
    ┌──────────────────────────────────────────────────────────────────────┐
    │                                                                      │
    │  ┌─────────┐    ┌───────────────────────────────────────────┐       │
    │  │  ALB    │    │     Kafka (KRaft, single broker on ECS)   │       │
    │  │ :80     │    │                                           │       │
    │  └────┬────┘    │  metrics-topic (6 partitions)             │       │
    │       │         │  analytics-output-topic (3 partitions)    │       │
    │       ▼         │  infra-events-topic (1 partition)         │       │
    │  ┌──────────┐   └──────┬──────────────────┬─────────────────┘       │
    │  │ cart-api  │──publish──┘                  │                        │
    │  │ (2-8     │          ┌──────────consume───┤                        │
    │  │  tasks)  │          │                    │                        │
    │  │ :8080    │          ▼                    ▼                        │
    │  └──────────┘   ┌────────────┐      ┌────────────┐                  │
    │                 │ analytics  │      │   alert    │                  │
    │                 │ (1 task)   │      │ (1 task)   │                  │
    │                 │ :8081      │      │ :8082      │                  │
    │                 └──────┬─────┘      └──────┬─────┘                  │
    │                        │                    │                        │
    │              publish   │                    │  SNS email             │
    │              to Kafka  │                    ▼                        │
    │                        │            ┌────────────┐                  │
    │                        │            │    SNS     │                  │
    │                        │            │  (alerts)  │                  │
    │                        ▼            └────────────┘                  │
    │                 ┌────────────┐                                       │
    │                 │  ai-agent  │  (ECS deployment)                    │
    │                 │  (1 task)  │────terraform apply──► ECS scaling    │
    │                 │  :8083     │                                       │
    │                 └────────────┘                                       │
    │                                                                      │
    │  ┌──────────────┐     ┌──────────────────┐                          │
    │  │  Prometheus  │     │    DynamoDB       │                          │
    │  │  (1 task)    │     │ cs6650-final-     │                          │
    │  │  :9090       │     │   metrics         │                          │
    │  └──────┬───────┘     └──────────────────┘                          │
    │         │ NLB (public)                                               │
    └─────────┼────────────────────────────────────────────────────────────┘
              │
              ▼
    ┌──────────────────┐        ┌──────────────────────┐
    │  Grafana (:3000) │◄───────│ Local Prometheus      │
    │  (local)         │        │ (scrapes ALB +        │
    └──────────────────┘        │  remote Prometheus)   │
                                └──────────────────────┘

    ┌──────────────────┐
    │  Locust          │──── HTTP POST /cart/items ────► ALB
    │  (load tester)   │
    └──────────────────┘

    ┌──────────────────┐        (Alternative AI agent approach)
    │  Monitor (local) │──polls DynamoDB──► detects alerts
    │  Go binary       │──spawns Claude Code CLI──► terraform apply
    └──────────────────┘
```

### Data Flow

The system implements a **five-stage data pipeline** with a feedback loop:

| Stage | Component | Input | Output |
|-------|-----------|-------|--------|
| 1. Generation | cart-api | HTTP requests (Locust) | MetricEvent to `metrics-topic` |
| 2. Aggregation | analytics | `metrics-topic` events | AggregatedMetric to `analytics-output-topic` + DynamoDB |
| 3. Alerting | alert | `metrics-topic` events | SNS email notifications |
| 4. Decision | ai-agent | `analytics-output-topic` | ScalingDecision to `infra-events-topic` |
| 5. Execution | ai-agent / Terraform | ScalingDecision | ECS desired count change via `terraform apply` |

The feedback loop closes when the Terraform apply changes the ECS desired count, which causes AWS to launch or terminate cart-api tasks, which changes the CPU utilization per task, which changes the metrics flowing through the pipeline, which may trigger further scaling decisions.

---

## 3. Technology Choices & Justification

### Go (over Java, Python, Node.js)

| Criterion | Go | Java | Python | Node.js |
|-----------|-----|------|--------|---------|
| Binary size | ~10 MB (static) | 200+ MB (JRE) | N/A (runtime) | N/A (runtime) |
| Startup time | <100ms | 2-5s (JVM warmup) | 500ms+ | 200ms+ |
| Concurrency model | Goroutines (M:N) | Threads (1:1) | GIL-limited | Event loop |
| Memory footprint | ~15 MB RSS | 200+ MB | 50+ MB | 50+ MB |

Go was selected for **all five microservices** because ECS Fargate charges per-second for allocated CPU/memory. Go's small binary size (multi-stage Docker builds produce ~10 MB images), near-instant startup, and low memory footprint (our tasks run at 256 CPU / 512 MB) minimize cold-start latency and resource waste. The goroutine-based concurrency model maps naturally to our workload: each Kafka consumer, HTTP handler, and background aggregation loop runs as a lightweight goroutine without thread pool tuning.

### Apache Kafka with KRaft Mode (over Redis Pub/Sub, SQS, RabbitMQ)

| Criterion | Kafka (KRaft) | Redis Pub/Sub | SQS | RabbitMQ |
|-----------|---------------|---------------|-----|----------|
| Consumer groups | Native | Manual | Native | Native |
| Partitioned parallelism | Yes (6 partitions) | No | Limited (FIFO) | Limited |
| Message persistence | Configurable retention | Fire-and-forget | 14 days max | Ack-based |
| Replayability | Yes (offset reset) | No | Limited | Limited |
| Ordering guarantee | Per-partition | None | Per-group | Per-queue |

Kafka was chosen for its **consumer group semantics** and **partitioned parallelism**. The `metrics-topic` has 6 partitions, allowing the analytics and alert services to independently consume the same data stream at their own pace via separate consumer groups (`analytics-group`, `alert-group`). KRaft mode eliminates the ZooKeeper dependency, simplifying the deployment from a 4-node cluster (3 Kafka + 1 ZK) to a single self-contained broker on ECS. This was critical given our lab account resource constraints.

Redis Pub/Sub was rejected because it is fire-and-forget with no persistence; if a consumer is temporarily down, messages are lost. SQS was rejected because it does not support the fan-out pattern (one message consumed by multiple independent consumers) without additional SNS fan-out configuration, and its FIFO queues have a 300 msg/s throughput limit. RabbitMQ was a viable alternative but lacks Kafka's log-based persistence and replay capability, which are valuable for debugging and the AI agent's historical analysis.

### AWS ECS Fargate (over EKS, EC2, Lambda)

| Criterion | ECS Fargate | EKS | EC2 | Lambda |
|-----------|-------------|-----|-----|--------|
| Cluster management | None | Control plane + nodes | Full | None |
| Container support | Native | Native | Manual | Limited |
| Billing model | Per-task-second | Per-node-hour + control plane | Per-instance-hour | Per-invocation |
| Auto-scaling | Application Auto Scaling | HPA + Cluster Autoscaler | ASG | Automatic |
| Cold start | 30-60s task launch | Similar | N/A | 100ms-10s |

ECS Fargate was chosen because it provides **serverless container orchestration** without cluster management overhead. Unlike EKS, there is no $0.10/hr control plane cost or node group management. Unlike raw EC2, we do not need to manage AMIs, patch OS, or configure Docker. Lambda was rejected because our services are long-running Kafka consumers that must maintain persistent TCP connections to brokers; Lambda's invocation model (max 15 min) and cold start behavior are incompatible.

The per-task-second billing model aligns well with our auto-scaling experiments: when the AI agent scales down from 8 to 2 tasks, billing immediately decreases.

### Terraform (over CloudFormation, CDK, Pulumi)

| Criterion | Terraform | CloudFormation | CDK | Pulumi |
|-----------|-----------|----------------|-----|--------|
| Language | HCL (declarative) | JSON/YAML | TypeScript/Python | TypeScript/Python |
| Cloud-agnostic | Yes | AWS only | AWS only | Yes |
| AI agent compatibility | Excellent (edit .tfvars) | Poor (complex JSON) | Poor (code generation) | Moderate |
| Plan/preview | `terraform plan` | Change sets | `cdk diff` | `pulumi preview` |

Terraform was the critical choice for the AI agent integration. The AI agent needs to **programmatically modify infrastructure parameters and apply changes**. Terraform's separation of variable definitions (`variables.tf`) from variable values (`terraform.tfvars`) creates a clean interface: the agent reads the current `ai_desired_count` from `terraform.tfvars`, computes a new value, writes it back, and runs `terraform apply`. This is a simple file edit + CLI invocation, far simpler than generating valid CloudFormation JSON or synthesizing CDK code.

The `terraform plan` command also provides a safety preview that the agent can log before applying.

### Prometheus + Grafana (over CloudWatch-only, Datadog)

| Criterion | Prometheus + Grafana | CloudWatch | Datadog |
|-----------|---------------------|------------|---------|
| Cost | Free (open source) | $0.30/metric/month | $15/host/month |
| Custom metrics | Unlimited | $0.30 each | Unlimited ($$) |
| Query language | PromQL (powerful) | CloudWatch Insights | DQL |
| Vendor lock-in | None | AWS | Datadog |
| Lab account compatible | Yes (self-hosted) | Yes (limited free tier) | Requires credit card |

Prometheus was chosen for its **zero-cost custom metrics**. Each of our services exposes 5-15 Prometheus metrics (request counts, latency histograms, Kafka publish rates, consumer lag, aggregation counts, alert counts). At $0.30/custom metric/month on CloudWatch, this would cost $30-50/month. Prometheus is free, self-hosted on a single ECS task, and provides PromQL -- a powerful query language that supports rate calculations, percentile aggregations, and label-based filtering that CloudWatch Metrics Insights cannot match.

Grafana was chosen over the Prometheus built-in UI for its dashboard provisioning (JSON-as-code), alerting capabilities, and multi-datasource support (we also query DynamoDB directly via the AWS plugin).

### DynamoDB (over RDS, Redis, S3)

| Criterion | DynamoDB | RDS PostgreSQL | Redis | S3 |
|-----------|----------|----------------|-------|-----|
| Billing model | Pay-per-request | Per-hour (always on) | Per-hour | Per-request + storage |
| Schema management | None | Migrations | None | None |
| Write latency | <10ms | 5-20ms | <1ms | 50-200ms |
| Serverless | Yes | Aurora Serverless ($$$) | ElastiCache ($$$) | Yes |

DynamoDB was chosen for **analytics persistence** because it is fully serverless with pay-per-request billing, requires no schema management, and supports low-latency writes. The analytics service writes one aggregated metric record every 10 seconds (6/minute), which falls well within DynamoDB's free tier (25 WCU). The `timestamp` string serves as the hash key, providing natural ordering for time-series queries.

RDS was rejected because even the smallest `db.t3.micro` instance costs $0.017/hr ($12/month) whether or not it is being used. Redis (ElastiCache) was rejected for the same always-on cost reason. S3 was rejected because its write latency (50-200ms) would add unnecessary delay to the aggregation loop.

### SNS (over SES, SendGrid)

SNS was chosen for alert notifications because **SES is not available in our AWS lab account** (SES requires domain verification and production access approval). SNS provides a simple publish/subscribe model that supports email delivery out of the box. The alert service publishes structured alert messages to an SNS topic (`cs6650-final-alerts`), which fans out to subscribed email addresses. SNS is native to AWS, requires no external accounts, and works within the lab's IAM constraints via the pre-existing LabRole.

### Locust (over JMeter, k6, wrk)

| Criterion | Locust | JMeter | k6 | wrk |
|-----------|--------|--------|-----|-----|
| Language | Python | Java/XML | JavaScript | C/Lua |
| Programmable profiles | Native (Python classes) | Limited (XML) | JavaScript | Lua scripts |
| Distributed testing | Built-in | Built-in | Cloud ($) | No |
| Real-time UI | Yes (web) | Yes (GUI) | No | No |

Locust was chosen because it allows **defining load profiles as Python classes**. Our four test profiles (SteadyUser, RampUser, SpikeUser, StepUser) are each a Python class with custom `wait_time` functions that produce distinct load patterns. The `SpikeUser` alternates between calm (1-2s wait) and burst (10-50ms wait) phases every 30 seconds using a simple time-based modulo -- this would require complex XML configuration in JMeter. Locust also supports distributed testing for scaling beyond a single machine's network capacity.

### Claude Code CLI (over Claude API, GPT API)

| Criterion | Claude Code CLI | Claude API | GPT API |
|-----------|----------------|------------|---------|
| Cost | Included (subscription) | $3-15 per 1M tokens | $2.50-10 per 1M tokens |
| Tool access | Full (AWS CLI, terraform, file editing) | None (text only) | Function calling |
| Execution model | Acts as real operator | Returns text suggestions | Returns function calls |
| Safety | Built-in permission system | Manual validation | Manual validation |

Claude Code CLI was preferred as the primary AI agent approach because it **requires no API credits** and provides **full tool access**. When spawned with a prompt, Claude Code can execute AWS CLI commands (`aws ecs describe-services`), read and write files (`terraform.tfvars`), and run Terraform -- acting as a real DevOps operator rather than a text advisor. The Claude API approach was also implemented as a containerized alternative for ECS deployment, but it requires API credits and can only suggest changes (the Go code must implement the actual file editing and Terraform execution).

---

## 4. Microservice Design Details

### 4.1 Cart API (`services/cart-api/`)

**Purpose:** HTTP API that simulates an e-commerce shopping cart. Serves as the **metric producer** for the entire monitoring pipeline.

**Input/Output:**

| Interface | Details |
|-----------|---------|
| HTTP IN | `POST /cart/items` -- add item to cart |
| HTTP IN | `GET /health` -- health check for ALB target group |
| HTTP IN | `GET /metrics` -- Prometheus metrics endpoint |
| Kafka OUT | `metrics-topic` -- publishes MetricEvent per request |

**Key Design Decisions:**

- **In-memory cart state** (`map[int]*Cart` keyed by `customer_id`): No database dependency. The cart is not the point of the demo; the metrics pipeline is. This eliminates an entire category of failure modes and deployment complexity.

- **CPU simulation via `burnCPU()`**: Each request performs 100 iterations of SHA-256 hashing, consuming approximately 0.5ms of CPU time per request. At 200 req/s on a 0.25 vCPU Fargate task, this produces ~40% CPU utilization. At 500 req/s, it hits 80-100%. This creates a realistic relationship between load and CPU usage that the AI agent can reason about.

- **Real CPU measurement via `syscall.Getrusage`**: The `cpuTracker` background goroutine samples actual process CPU time every 5 seconds using the `Getrusage` syscall, computing CPU utilization as a percentage of the allocated 0.25 vCPU. This replaced an earlier approach that used goroutine count as a heuristic (which always reported ~100% and was useless).

- **Async Kafka publishing** (`go h.producer.PublishMetric(...)` in handler): The metric publish runs in a background goroutine so it does not add Kafka latency to the HTTP response path. The `kafka.Writer` is configured with `BatchTimeout: 10ms` and `RequiredAcks: RequireOne` for a balance between throughput and reliability.

**MetricEvent schema:**

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

**Failure Handling:**
- Kafka publish failures are logged and counted via `KafkaPublishTotal` with a `"failure"` label, but do not fail the HTTP request. The system degrades gracefully: if Kafka is down, HTTP requests succeed but metrics are lost.
- Graceful shutdown via `signal.Notify(SIGINT, SIGTERM)` with a 5-second context timeout ensures in-flight requests complete and the Kafka writer flushes before exit.

**Prometheus Metrics Exported:**

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `cart_api_http_requests_total` | Counter | method, endpoint, status | Request volume and error rate |
| `cart_api_http_request_duration_seconds` | Histogram | method, endpoint, status | Latency percentiles (p50, p95, p99) |
| `cart_api_kafka_publish_total` | Counter | result (success/failure) | Kafka reliability tracking |

### 4.2 Analytics Service (`services/analytics/`)

**Purpose:** Consumes raw metric events, computes rolling-window aggregations, publishes aggregated summaries for the AI agent, and persists results to DynamoDB.

**Input/Output:**

| Interface | Details |
|-----------|---------|
| Kafka IN | `metrics-topic` (consumer group: `analytics-group`) |
| Kafka OUT | `analytics-output-topic` -- publishes AggregatedMetric every 10s |
| DynamoDB OUT | `cs6650-final-metrics` table -- persists every aggregation |
| HTTP IN | `GET /metrics`, `GET /health` |

**Key Design Decisions:**

- **60-second rolling window**: The `Aggregator` maintains a sliding window of raw events. Every 10 seconds, it prunes events older than 60 seconds, then computes aggregates over the remaining events. The 60-second window smooths out short-lived noise (individual slow requests, GC pauses) while remaining responsive enough to detect genuine load changes.

- **Trend detection**: The aggregator compares the current window's average CPU to the previous window's average. A difference greater than +5 percentage points is classified as "increasing", less than -5 as "decreasing", otherwise "stable". This simple heuristic provides the AI agent with directional information about load trajectory.

- **P99 latency calculation**: Latencies are sorted and the 99th percentile is extracted via index calculation (`ceil(0.99 * N) - 1`). This gives the AI agent a tail-latency signal that average latency would mask.

- **Dual output (Kafka + DynamoDB)**: Every aggregation is both published to Kafka (for the AI agent's real-time consumption) and written to DynamoDB (for persistence and the local monitor's polling). This decouples the two AI agent approaches: the ECS-deployed agent consumes from Kafka, while the local monitor polls DynamoDB.

**AggregatedMetric schema:**

```json
{
  "timestamp": "2026-03-28T12:01:00Z",
  "window_seconds": 60,
  "avg_cpu_percent": 67.3,
  "max_cpu_percent": 89.1,
  "avg_latency_ms": 45.0,
  "p99_latency_ms": 120.0,
  "request_rate_per_sec": 5.83,
  "error_rate_percent": 0.5,
  "trend": "increasing",
  "instance_count": 3
}
```

**Failure Handling:**
- DynamoDB write failures are logged but do not block the Kafka publish or the aggregation loop. If DynamoDB is unavailable, the Kafka pipeline continues unaffected.
- The DynamoDB writer is initialized with a nil check; if the `DYNAMODB_TABLE` environment variable is empty (local development), DynamoDB persistence is silently disabled.

### 4.3 Alert Service (`services/alert/`)

**Purpose:** Evaluates raw metric events against threshold rules and sends email alerts via SNS when thresholds are breached.

**Input/Output:**

| Interface | Details |
|-----------|---------|
| Kafka IN | `metrics-topic` (consumer group: `alert-group`) |
| SNS OUT | `cs6650-final-alerts` topic -- email notifications |
| HTTP IN | `GET /metrics`, `GET /health` |

**Key Design Decisions:**

- **Per-instance windowed evaluation**: The `Evaluator` maintains a separate 30-second sliding window of samples for each `instance_id`. This prevents a single overloaded instance from being diluted by healthy instances in a multi-task deployment.

- **Three alert types with distinct logic**:

  | Alert Type | Condition | Window Requirement |
  |------------|-----------|-------------------|
  | `HIGH_CPU` | All samples > 80% CPU | Window spans 30+ seconds (sustained) |
  | `HIGH_ERROR_RATE` | Error rate > 5% (status >= 500) | Any samples in window |
  | `HIGH_LATENCY` | P99 latency > 500ms | Any samples in window |

- **5-minute cooldown deduplication**: After firing an alert for a specific `instance_id:alert_type` combination, subsequent alerts of the same type from the same instance are suppressed for 5 minutes. This prevents alert fatigue during sustained high-load periods.

- **Dual notification (stdout + SNS)**: Every alert is logged as structured JSON to stdout (captured by CloudWatch Logs) regardless of SNS configuration. If the `SNS_TOPIC_ARN` environment variable is set, the alert is also published to SNS. This ensures alerts are always observable even if SNS is misconfigured.

**Failure Handling:**
- SNS publish failures are logged but do not crash the service. The alert is still recorded in stdout/CloudWatch.
- If AWS config cannot be loaded, SNS is disabled with a warning log, and the service continues with stdout-only alerting.

### 4.4 AI DevOps Agent (`services/ai-agent/`) -- ECS Deployment

**Purpose:** Consumes aggregated metrics from the analytics service, consults Claude for scaling decisions, and applies infrastructure changes via Terraform.

**Input/Output:**

| Interface | Details |
|-----------|---------|
| Kafka IN | `analytics-output-topic` (consumer group: `ai-agent-group`) |
| Kafka OUT | `infra-events-topic` -- publishes ScalingDecision audit records |
| Terraform | Reads/writes `terraform.tfvars`, executes `terraform plan` + `terraform apply` |
| Claude API | POST `https://api.anthropic.com/v1/messages` (claude-sonnet-4-20250514) |
| HTTP IN | `GET /metrics`, `GET /status`, `GET /health` |

**Key Design Decisions:**

- **Structured JSON response from Claude**: The system prompt constrains Claude to respond with **only valid JSON** (no markdown, no explanation outside JSON). The response is parsed into a `ClaudeResponse` struct with `action`, `reasoning`, `changes`, and `confidence` fields. This eliminates free-text parsing ambiguity.

- **History buffer (5 windows)**: The agent maintains the last 5 aggregated metric windows. This gives Claude context about load trajectory -- not just the current snapshot. A single high-CPU reading is less actionable than 5 consecutive increasing readings.

- **Safety guardrails**: See Section 7 for detailed guardrail design.

- **Terraform integration via `terraform.tfvars` editing**: The agent uses regex replacement to update `ai_desired_count` and `scaling_mode` in the tfvars file, then shells out to `terraform plan` followed by `terraform apply -auto-approve`. This approach is auditable (git diff shows exactly what changed) and reversible.

**Failure Handling:**
- Claude API errors (timeout, rate limit, invalid response) are logged but do not crash the agent. The metric is effectively skipped, and the next aggregation window will trigger a new decision attempt.
- Terraform apply failures are logged and counted via `MetricsTerraformApplies` with a `"failure"` label. The agent does not retry automatically -- it waits for the next aggregation window.
- Invalid Claude responses (malformed JSON, out-of-bounds task counts, invalid actions) are caught by `validateDecision()` and rejected.

### 4.5 Monitor Service (`services/monitor/`) -- Local Claude Code Approach

**Purpose:** Alternative AI agent that runs locally, polls DynamoDB for new metrics, evaluates alert conditions, and spawns Claude Code CLI for autonomous infrastructure management.

**Input/Output:**

| Interface | Details |
|-----------|---------|
| DynamoDB IN | Polls `cs6650-final-metrics` table every 10 seconds |
| Claude Code OUT | Spawns `claude --print --dangerously-skip-permissions` with structured prompt |
| Terraform | Claude Code directly edits files and runs terraform |

**Key Design Decisions:**

- **DynamoDB polling instead of Kafka**: The monitor runs locally (not on ECS), so it cannot consume from the internal Kafka cluster. Instead, it polls DynamoDB where the analytics service persists every aggregation. The `lastTimestamp` field prevents processing the same metric twice.

- **Alert evaluation logic**: The monitor implements its own threshold evaluation (similar but distinct from the alert service), checking for:
  - `HIGH_CPU_INCREASING`: CPU > 70% with increasing trend
  - `SUSTAINED_HIGH_CPU`: CPU > 80% for 3+ consecutive windows
  - `SUSTAINED_LOW_CPU`: CPU < 30% for 3+ consecutive windows
  - `HIGH_ERROR_RATE`: Error rate > 5%
  - `HIGH_LATENCY`: P99 > 500ms

- **Claude Code as the execution engine**: Instead of calling the Claude API and implementing Terraform execution in Go, the monitor spawns Claude Code CLI with a detailed prompt that includes current metrics, history, and step-by-step instructions for checking AWS state and applying Terraform changes. Claude Code has full tool access (AWS CLI, file editing, Terraform) and acts as a real DevOps operator.

---

## 5. Infrastructure Design

### VPC and Networking

```
VPC: 10.0.0.0/16
├── Public Subnet A: 10.0.1.0/24 (us-west-2a)
├── Public Subnet B: 10.0.2.0/24 (us-west-2b)
├── Internet Gateway
├── Route Table: 0.0.0.0/0 → IGW
│
├── ALB (public, application)
│   └── Listener :80 → cart-api target group (:8080, IP-based)
│
├── Kafka NLB (internal, network)
│   └── Listener :9092 → kafka target group (:9092, IP-based)
│
└── Prometheus NLB (public, network)
    └── Listener :9090 → prometheus target group (:9090, IP-based)
```

All ECS tasks run in public subnets with public IP assignment enabled. This is a simplification driven by the lab account's constraints -- private subnets would require NAT gateways (additional cost) or VPC endpoints for ECR/CloudWatch/DynamoDB access.

Two availability zones (us-west-2a, us-west-2b) provide redundancy for the ALB and NLB. The ALB distributes cart-api traffic across tasks in both AZs.

### ECS Task Definitions

| Service | CPU (units) | Memory (MiB) | Desired Count | Launch Type |
|---------|-------------|---------------|---------------|-------------|
| kafka | 1024 | 2048 | 1 | FARGATE |
| cart-api | 256 | 512 | 2-8 (auto-scaled) | FARGATE |
| analytics | 256 | 512 | 1 | FARGATE |
| alert | 256 | 512 | 1 | FARGATE |
| prometheus | 512 | 1024 | 1 | FARGATE |

### Auto-Scaling: Static vs. AI Modes

The system supports two mutually exclusive scaling modes, controlled by the `scaling_mode` variable in `terraform.tfvars`:

**Static Mode (`scaling_mode = "static"`):**

```hcl
resource "aws_appautoscaling_target" "cart_api" {
  count = var.scaling_mode == "static" ? 1 : 0
  # ...
}

resource "aws_appautoscaling_policy" "cart_api_cpu" {
  count = var.scaling_mode == "static" ? 1 : 0
  # Target tracking: ECSServiceAverageCPUUtilization at 70%
  # Scale-in cooldown: 300s
  # Scale-out cooldown: 300s
  # Min: 1, Max: 8
}
```

AWS Application Auto Scaling manages the desired count automatically based on CloudWatch's `ECSServiceAverageCPUUtilization` metric. When average CPU across all cart-api tasks exceeds 70%, AWS adds tasks. When it drops below 70%, tasks are removed (after 300s cooldown).

**AI Mode (`scaling_mode = "ai"`):**

When `scaling_mode = "ai"`, the `aws_appautoscaling_target` and `aws_appautoscaling_policy` resources are **not created** (conditional `count = 0`). The AI agent directly controls `ai_desired_count` in `terraform.tfvars` and applies via Terraform. The ECS service's `desired_count` is set to `local.desired_count`, which reads from `var.ai_desired_count` in AI mode.

This design ensures the two approaches never conflict: either AWS auto-scaling is active, or the AI agent is in control, never both.

### Security Groups

```
┌─────────────────────────────────────────────────┐
│ ALB Security Group                              │
│ Ingress: 0.0.0.0/0 :80 (HTTP from anywhere)    │
│ Egress: 0.0.0.0/0 all (to ECS tasks)           │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────┐
│ ECS Tasks Security Group (shared by all tasks)  │
│                                                 │
│ Ingress rules:                                  │
│  1. ALB SG → :8080 (cart-api container port)    │
│  2. Self → :0-65535 (inter-task: Kafka, etc.)   │
│  3. VPC CIDR → :9092 (NLB pass-through Kafka)   │
│  4. 0.0.0.0/0 → :9090 (Prometheus NLB)         │
│                                                 │
│ Egress: 0.0.0.0/0 all                          │
└─────────────────────────────────────────────────┘
```

A single security group is shared by all ECS tasks with a `self` ingress rule that allows all TCP traffic between tasks. This is necessary because Kafka consumers (analytics, alert, ai-agent) need to reach the Kafka broker on port 9092 via the internal NLB.

The VPC CIDR ingress rule for port 9092 is required because NLBs preserve client source IPs (unlike ALBs). When an ECS task connects to the Kafka NLB, the Kafka container sees the task's VPC IP, not the NLB's IP. The `self` rule only covers IPs assigned to tasks in the same security group; the VPC CIDR rule provides broader coverage for NLB-routed traffic.

### IAM: LabRole Constraints and Workarounds

The AWS lab account provides a single pre-existing IAM role (`LabRole`) that cannot be modified. Custom IAM roles, policies, and instance profiles cannot be created. All ECS task definitions use `LabRole` for both `execution_role_arn` (ECR pull, CloudWatch logs) and `task_role_arn` (DynamoDB, SNS, etc.).

This constraint eliminates the principle of least privilege -- every task has the same broad permissions. In a production environment, each service would have a dedicated task role with only the permissions it needs (e.g., analytics gets `dynamodb:PutItem` only, alert gets `sns:Publish` only).

### Service Discovery: Internal NLB for Kafka

AWS Cloud Map (ECS Service Discovery) is not available in the lab account. Without service discovery, Kafka consumers cannot resolve the Kafka broker's dynamic IP address (Fargate tasks get new IPs on every deployment).

The solution is an **internal Network Load Balancer** that provides a stable DNS name (`cs6650-final-kafka-nlb-*.elb.us-west-2.amazonaws.com`). All ECS task definitions reference this NLB DNS name in their `KAFKA_BROKERS` environment variable. Kafka's `KAFKA_ADVERTISED_LISTENERS` is also set to the NLB DNS, so the broker tells clients to reconnect through the NLB.

---

## 6. Monitoring & Observability

### Prometheus Architecture

The system uses a **two-tier Prometheus architecture**:

1. **ECS Prometheus** (remote, running on Fargate): Scrapes the cart-api `/metrics` endpoint via the ALB. Deployed as a custom Docker image with the scrape config baked in (`prometheus-ecs.yml`). Exposed via a public NLB so local Grafana can query it.

2. **Local Prometheus** (`docker-compose.grafana.yml`): Runs alongside Grafana on the developer's machine. Scrapes:
   - The remote cart-api via ALB (`cs6650-final-alb-*.elb.amazonaws.com:80/metrics`)
   - The local AI agent / monitor service (`:8083/metrics`)

```yaml
# prometheus-local-agent.yml
scrape_configs:
  - job_name: "ai-agent"
    static_configs:
      - targets: ["host.docker.internal:8083"]
  - job_name: "cart-api"
    metrics_path: /metrics
    static_configs:
      - targets: ["cs6650-final-alb-*.us-west-2.elb.amazonaws.com:80"]
```

### Grafana Dashboards

**System Overview Dashboard** (`system-overview.json`):

| Panel | Metric / Query | Purpose |
|-------|---------------|---------|
| CPU Usage | `rate(process_cpu_seconds_total[1m]) / 2 * 100` | Per-task CPU as % of available host CPUs |
| Memory RSS | `process_resident_memory_bytes` | Memory pressure detection |
| HTTP Request Rate | `rate(cart_api_http_requests_total[1m])` | Throughput monitoring |
| Latency Percentiles | `histogram_quantile(0.5/0.95/0.99, rate(cart_api_http_request_duration_seconds_bucket[1m]))` | Tail latency detection |
| Goroutines | `go_goroutines` | Concurrency / leak detection |
| Kafka Publish Rate | `rate(cart_api_kafka_publish_total{result="success"}[1m])` | Pipeline health |
| Error Rate | `rate(cart_api_kafka_publish_total{result="failure"}[1m])` | Kafka connectivity |

**AI Agent Audit Dashboard** (planned):

| Panel | Source | Purpose |
|-------|--------|---------|
| Decision Timeline | `infra-events-topic` | Chronological scaling decisions |
| Reasoning Text | `infra-events-topic` | Claude's explanation for each decision |
| Task Count History | `ai_agent_current_task_count` | Before/after task count over time |
| Confidence Scores | `ai_agent_decisions_total` | Distribution of decision confidence |
| Apply Latency | `ai_agent_decision_latency_seconds` | Time from metric receipt to terraform apply |

### CPU Calibration Challenge

Accurately measuring CPU utilization in a Fargate container was one of the most significant engineering challenges in the project. We went through three iterations:

**Version 1 -- Goroutine heuristic (FAILED):**
The initial approach estimated CPU usage based on the number of active goroutines. This always reported ~100% because Go's runtime scheduler maintains a pool of goroutines for network polling, garbage collection, and other background tasks -- even an idle Go process shows 5-10 goroutines.

**Version 2 -- `syscall.Getrusage` (WORKING):**
The final approach uses the Linux `getrusage(RUSAGE_SELF)` syscall to read actual process CPU time (user + system). A background goroutine samples every 5 seconds:

```go
func (ct *cpuTrackerState) sample() {
    var usage syscall.Rusage
    syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
    cpuTime := tvToDuration(usage.Utime) + tvToDuration(usage.Stime)
    cpuDelta := (cpuTime - ct.lastCPU).Seconds()
    elapsed := now.Sub(ct.lastTime).Seconds()
    // CPU as % of allocated vCPU (0.25 cores for 256 CPU units)
    ct.currentPct = (cpuDelta / elapsed / ct.allocatedCPU) * 100.0
}
```

**Key calibration parameter: `allocatedCPU = 0.25`**

ECS Fargate allocates CPU in "CPU units" where 1024 units = 1 vCPU. Our cart-api tasks use 256 CPU units = 0.25 vCPU. The `cpuDelta / elapsed` calculation gives CPU cores consumed (e.g., 0.20 cores). Dividing by `allocatedCPU` (0.25) converts this to **percentage of allocated capacity** (80%), which is the semantically meaningful number for scaling decisions.

**Grafana PromQL for CPU:**

```
rate(process_cpu_seconds_total[1m]) / 2 * 100
```

The `/ 2` divisor accounts for Fargate's host having 2 available CPU cores. The `process_cpu_seconds_total` metric (exported by the Go Prometheus client library) reports total CPU seconds consumed. The `rate()` function computes cores used, and `/ 2 * 100` converts to percentage of available host CPUs.

**Fargate CPU burst behavior:** Fargate tasks can temporarily use more CPU than allocated. A 256-unit task (0.25 vCPU) can burst to 2 full cores if the host has spare capacity. This means the `Getrusage`-based metric can report >100% during bursts, which we cap at 100% in the code. The Grafana `process_cpu_seconds_total` metric can also show values above expected due to bursting.

---

## 7. AI DevOps Agent Design

### Two Implementation Approaches

We implemented two distinct AI agent architectures to explore different trade-offs:

#### Approach 1: Claude API Service (ECS-deployed)

```
analytics-output-topic → ai-agent (Go service on ECS)
                              │
                              ├── Claude API (claude-sonnet-4-20250514)
                              │     └── Returns JSON: {action, reasoning, changes, confidence}
                              │
                              ├── terraform.go: edit terraform.tfvars
                              │
                              ├── terraform plan + terraform apply
                              │
                              └── infra-events-topic (audit log)
```

This approach runs as a containerized Go service on ECS Fargate. It consumes aggregated metrics from Kafka, calls the Claude Messages API with a structured prompt, parses the JSON response, and applies Terraform changes. The Go code handles all file I/O and process execution.

**Pros:** Fully automated, runs 24/7 on ECS, low latency (30s API call)
**Cons:** Requires API credits ($3-15/MTok), limited to text responses (cannot run AWS CLI to verify state)

#### Approach 2: Claude Code CLI Monitor (local)

```
DynamoDB ← polls ← monitor (Go binary, local)
                      │
                      ├── Evaluates alert thresholds
                      │
                      └── Spawns: claude --print --dangerously-skip-permissions "<prompt>"
                              │
                              ├── Claude Code reads AWS state (aws ecs describe-services)
                              ├── Claude Code checks CloudWatch logs
                              ├── Claude Code edits terraform.tfvars
                              └── Claude Code runs terraform apply
```

This approach runs a Go binary locally that polls DynamoDB for new metrics. When alert conditions are detected, it spawns Claude Code CLI with a detailed prompt. Claude Code has full access to the local filesystem, AWS CLI, and Terraform -- it acts as an autonomous DevOps operator.

**Pros:** No API credits, full tool access, can inspect real AWS state before deciding
**Cons:** Requires local machine running, higher latency (60-120s per invocation)

### Why Claude Code Was Preferred

1. **No API credits required**: The Claude Code CLI is included with the Claude subscription. API usage at our load testing scale (hundreds of decisions) would cost $10-50.

2. **Full tool access**: Claude Code can run `aws ecs describe-services` to check current running count, `aws logs filter-log-events` to read recent logs, and `aws dynamodb scan` to verify metric trends -- all before making a decision. The API approach can only reason about the metrics passed in the prompt.

3. **Acts as a real operator**: Claude Code performs the same steps a human DevOps engineer would: check the dashboard, verify the alert, inspect logs, edit the config, run terraform plan, review the plan, then apply. This produces more reliable decisions because the agent can validate its assumptions against real AWS state.

### Prompt Engineering

The Claude Code monitor constructs a structured prompt with five sections:

```
1. ALERTS TRIGGERED
   - [HIGH_CPU_INCREASING] CPU at 78.5% with increasing trend

2. CURRENT METRICS (latest 10-second window)
   { "avg_cpu_percent": 78.5, "p99_latency_ms": 120, ... }

3. METRIC HISTORY (last 10 windows)
   [ { ... }, { ... }, ... ]

4. INSTRUCTIONS (step-by-step)
   1. Check ECS service status: aws ecs describe-services ...
   2. Check CloudWatch logs: aws logs filter-log-events ...
   3. Check DynamoDB trends: aws dynamodb scan ...
   4. Decide if scaling is needed (rules provided)
   5. If yes, edit terraform.tfvars and run terraform apply
   6. Report what you did and why

5. CONSTRAINTS
   - Min 1, max 8 tasks
   - Change by at most +-2 per decision
   - Be conservative
```

The Claude API approach uses a simpler system/user prompt pair:

- **System prompt**: "You are an infrastructure scaling advisor. Respond ONLY with valid JSON."
- **User prompt**: Current metrics, history, task count, and scaling rules with an explicit JSON schema to follow.

### Safety Guardrails

Both approaches implement multiple safety layers:

| Guardrail | Claude API Approach | Claude Code Approach |
|-----------|--------------------|--------------------|
| Confidence threshold | Only apply if `confidence >= 0.7` | Embedded in prompt ("only scale if data clearly supports it") |
| Max change per cycle | Clamped to +/-2 tasks in Go code | Stated in prompt ("change by at most +-2 per decision") |
| Bounds enforcement | `if newCount < 1 { newCount = 1 }; if newCount > 8 { newCount = 8 }` | Stated in prompt + Terraform variable validation |
| Cooldown period | 60 seconds between applies (configurable) | 120 seconds between Claude Code invocations |
| Dry-run mode | `DRY_RUN=true` logs decisions without applying | `--dry-run` flag prints prompt without invoking Claude Code |
| Action validation | `validateDecision()` rejects invalid action strings | Claude Code's built-in permission system |
| Terraform plan preview | Runs `terraform plan` before `apply` | Claude Code runs plan and reviews output before applying |

---

## 8. Challenges & Solutions

### Challenge 1: Lab Account IAM Restrictions

**Problem:** The AWS lab account cannot create IAM roles, policies, or instance profiles. ECS task definitions require both an `execution_role_arn` (for ECR pull and CloudWatch logs) and a `task_role_arn` (for application-level AWS API calls).

**Solution:** Used the pre-existing `LabRole` (ARN: `arn:aws:iam::<account_id>:role/LabRole`) for both roles across all task definitions. This role has broad permissions that cover ECR, CloudWatch, DynamoDB, SNS, and ECS operations.

**Trade-off:** No least-privilege isolation between services. Every task can access every AWS resource the LabRole permits.

### Challenge 2: Cloud Map Not Available

**Problem:** AWS Cloud Map (ECS Service Discovery) is not available in the lab account. Without it, Kafka consumers cannot discover the Kafka broker's IP address, which changes on every task restart (Fargate assigns new IPs).

**Solution:** Deployed an **internal Network Load Balancer** in front of the Kafka ECS service. The NLB provides a stable DNS name that all services reference. Kafka's `KAFKA_ADVERTISED_LISTENERS` is set to the NLB DNS so broker metadata responses direct clients back through the NLB.

**Trade-off:** NLB adds a small amount of latency (~1ms) and cost ($0.0225/hr). A proper setup would use Cloud Map for zero-cost DNS-based discovery.

### Challenge 3: SES Not Available

**Problem:** AWS SES (Simple Email Service) requires domain verification and production access approval, neither of which is possible in the lab account. The alert service originally planned to use SES for email notifications.

**Solution:** Replaced SES with **SNS** (Simple Notification Service). Created an SNS topic (`cs6650-final-alerts`) with an email subscription. SNS delivers alert messages as emails without requiring domain verification. The subscription confirmation email is sent automatically when the subscription is created.

**Trade-off:** SNS email messages have less formatting control than SES (no HTML templates, limited subject line). For our monitoring alerts, plain text is sufficient.

### Challenge 4: Kafka NLB Public vs. Internal

**Problem:** Initially, we considered making the Kafka NLB public so the local monitor could consume directly from Kafka. However, exposing Kafka to the public internet without authentication is a security risk, and Kafka's advertised listener mechanism means clients would need to resolve internal IPs from outside the VPC.

**Solution:** Made the Kafka NLB **internal** (only reachable from within the VPC). For the local AI agent, we used DynamoDB as an intermediary: the analytics service persists metrics to DynamoDB, and the local monitor polls DynamoDB. This cleanly separates the ECS-internal Kafka network from external access.

### Challenge 5: CPU Measurement Accuracy

**Problem:** The initial goroutine-counting heuristic for CPU measurement always reported ~100%, making it useless for the analytics service's aggregations and the AI agent's scaling decisions.

**Solution:** Replaced with `syscall.Getrusage(RUSAGE_SELF)` to read actual kernel-level CPU time. Added the `allocatedCPU = 0.25` parameter to normalize CPU usage as a percentage of the 256-unit (0.25 vCPU) Fargate allocation.

**Verification:** Under Locust load of 200 users with SteadyUser profile (~70 req/s), the cart-api reports 40-50% CPU. Under 500 users (~250 req/s), it reports 80-100%. These numbers match the expected behavior based on the `burnCPU()` SHA-256 loop timing.

### Challenge 6: Prometheus ECS Service Discovery

**Problem:** Prometheus v2.54 supports `ecs_sd_configs` for automatic ECS task discovery, but this feature requires the Prometheus container to have IAM permissions to call the ECS API (`ecs:ListTasks`, `ecs:DescribeTasks`). In the lab environment, the Prometheus task's LabRole permissions did not reliably support this, and the configuration was complex.

**Solution:** Used **static targets** pointing to the ALB DNS name:

```yaml
scrape_configs:
  - job_name: "cart-api"
    static_configs:
      - targets: ["cs6650-final-alb-*.us-west-2.elb.amazonaws.com:80"]
```

The ALB round-robins scrape requests across cart-api tasks, providing a representative sample of metrics. This is less ideal than per-task scraping (metrics are aggregated across tasks) but sufficient for our monitoring needs.

**Trade-off:** Prometheus sees metrics from a random cart-api task per scrape interval, not all tasks simultaneously. For task-specific analysis, CloudWatch Logs provides per-task data.

### Challenge 7: Kafka Advertised Listeners with NLB

**Problem:** Kafka requires `KAFKA_ADVERTISED_LISTENERS` to be set to an address that clients can reach. On ECS Fargate, the task's IP changes on every restart. If set to `localhost` or the task IP, external clients (other ECS tasks via NLB) cannot reconnect after a broker restart.

**Solution:** Set `KAFKA_ADVERTISED_LISTENERS` to the NLB DNS name:

```hcl
{ name = "KAFKA_ADVERTISED_LISTENERS", value = "PLAINTEXT://${aws_lb.kafka.dns_name}:9092" }
```

When clients connect to the NLB and request broker metadata, Kafka returns the NLB address as the broker endpoint, ensuring clients always route through the NLB.

---

## 9. Experiments (Planned)

### Experiment 1: Producer Scalability

| Parameter | Value |
|-----------|-------|
| **Objective** | Measure Kafka throughput as cart-api scales from 1 to 20 tasks |
| **Method** | Fix Locust at 1000 users (SteadyUser), scale cart-api tasks via Terraform |
| **Metrics** | Kafka messages/second, publish latency (p50/p99), error rate |
| **Expected** | Throughput scales linearly up to Kafka partition count (6), then plateaus |
| **Duration** | 3 min per task count, 10 steps |

### Experiment 2: Fault Tolerance (Kafka KRaft)

| Parameter | Value |
|-----------|-------|
| **Objective** | Measure system behavior when the Kafka broker fails |
| **Method** | `aws ecs update-service --desired-count 0` for Kafka, wait, restore |
| **Metrics** | Message loss, re-election time, consumer recovery time |
| **Expected** | Single-broker setup: full outage during failure, recovery after task restart |
| **Duration** | 5 min (2 min baseline, 1 min outage, 2 min recovery) |
| **Note** | On ECS we run single-broker KRaft; local docker-compose has 3 brokers for true fault tolerance testing |

### Experiment 3: Consumer Scalability

| Parameter | Value |
|-----------|-------|
| **Objective** | Measure analytics consumer lag as consumer count scales 1 to 5 |
| **Method** | Fix producer rate at 500 msg/s, scale analytics service tasks |
| **Metrics** | Consumer lag (messages), processing latency, partition rebalance time |
| **Expected** | Lag decreases linearly up to 6 consumers (matching partition count) |
| **Duration** | 3 min per consumer count |

### Experiment 4: Alert Latency

| Parameter | Value |
|-----------|-------|
| **Objective** | Measure end-to-end latency from CPU spike to email delivery |
| **Method** | Ramp Locust from 50 to 2000 users (SpikeUser profile) |
| **Metrics** | Time from first CPU > 80% event to SNS email received |
| **Expected** | <60s (30s alert window + Kafka latency + SNS delivery) |
| **Duration** | 5 min |

### Experiment 5: AI Scaling Decision Accuracy

| Parameter | Value |
|-----------|-------|
| **Objective** | Measure AI agent decision quality under known load patterns |
| **Method** | Run StepUser (200 -> 500 -> 800 -> 1000 users, 2 min each) with AI mode |
| **Metrics** | Decision latency, correct scaling direction rate, task count trajectory |
| **Expected** | Agent scales up at each step within 2 aggregation windows (20s) |
| **Duration** | 10 min |

### Experiment 6: Closed-Loop Stability

| Parameter | Value |
|-----------|-------|
| **Objective** | Verify the AI agent does not oscillate (repeatedly scaling up and down) |
| **Method** | 30-min sustained RampUser load, AI mode active |
| **Metrics** | Task count over time, scaling decision frequency, CPU convergence |
| **Expected** | System converges to stable task count within 5 min, <3 oscillations |
| **Duration** | 30 min |

### Experiment 7: Agent Failure Isolation

| Parameter | Value |
|-----------|-------|
| **Objective** | Verify that the monitoring pipeline continues if the AI agent fails |
| **Method** | Kill AI agent mid-cycle, observe system behavior for 5 min, restore |
| **Metrics** | Kafka consumer offsets, alert delivery, metric persistence |
| **Expected** | Kafka retains messages, analytics/alert services unaffected, agent resumes at committed offset |
| **Duration** | 10 min |

### Experiment 8: Static vs. AI Scaling Comparison

| Parameter | Value |
|-----------|-------|
| **Objective** | Quantitatively compare AWS auto-scaling vs. AI-driven scaling |
| **Method** | Run identical StepUser load profile under both `scaling_mode="static"` and `"ai"` |
| **Metrics** | Scaling reaction time, CPU utilization variance, over/under-provisioning |
| **Expected** | AI agent reacts faster to trends (proactive) vs. static (reactive to CPU threshold) |
| **Duration** | 10 min per mode |

---

## 10. Lessons Learned

### What Worked Well

1. **Go for microservices**: The choice of Go paid off significantly. All five services compile to small static binaries (<15 MB), Docker images are tiny (Alpine-based, ~25 MB), startup is near-instant, and memory usage is minimal (15-30 MB RSS). This matters on Fargate where you pay for allocated resources. The goroutine model also made concurrent Kafka consumer + HTTP server + background aggregation loops trivial to implement.

2. **Kafka as the central nervous system**: The consumer group model was exactly right for our fan-out pattern. Analytics and alert services independently consume the same `metrics-topic` at their own pace without coordination. Adding a new consumer (like the AI agent on `analytics-output-topic`) is a zero-downtime operation -- just deploy a new service with a new consumer group.

3. **Terraform.tfvars as the AI-infrastructure interface**: The separation between infrastructure definition (`.tf` files) and parameters (`.tfvars`) created a clean, simple interface for the AI agent. The agent only needs to read and write a text file with `key = value` pairs, then run two CLI commands. No SDK, no API -- just file I/O and process execution.

4. **Dual AI agent approaches**: Having both the Claude API approach (containerized, automated) and the Claude Code approach (local, tool-rich) gave us flexibility. The Claude Code approach was invaluable during development because it could inspect real AWS state and debug issues autonomously.

### What Did Not Work

1. **Goroutine-based CPU estimation**: Our initial CPU measurement approach (counting goroutines) was completely wrong. Goroutine count correlates poorly with CPU usage because most goroutines are blocked on I/O (network, channels, timers). This wasted a full day of debugging before switching to `Getrusage`.

2. **Single-broker Kafka on ECS**: Running a single Kafka broker on Fargate sacrifices the core value proposition of Kafka: replication and fault tolerance. If the broker task restarts, all consumers lose their connections and must reconnect. The 3-broker local setup (docker-compose) demonstrates true fault tolerance, but reproducing that on ECS within our budget and lab constraints was not feasible.

3. **Static Prometheus targets via ALB**: Scraping metrics through the ALB means Prometheus gets responses from random cart-api tasks on each scrape. This makes it impossible to track per-task metrics over time. A proper setup would use ECS service discovery or a service mesh sidecar for per-task scraping.

### What We Would Do Differently

1. **Use private subnets with NAT gateway**: All our ECS tasks have public IPs, which is a security concern. In a production environment, we would place tasks in private subnets with a NAT gateway for outbound internet access (ECR pulls, API calls) and use VPC endpoints for DynamoDB and CloudWatch.

2. **Multi-broker Kafka from the start**: We would deploy a 3-broker Kafka cluster on ECS from the beginning, using unique NLB listeners per broker (9092, 9093, 9094) to enable proper partition replication. The single-broker setup limited our fault tolerance experiments.

3. **OpenTelemetry instead of raw Prometheus**: Using the OpenTelemetry SDK with a Prometheus exporter would provide distributed tracing across services (request ID propagation through Kafka messages) in addition to metrics. This would make it much easier to trace the end-to-end latency from HTTP request to alert delivery.

4. **Structured logging from day one**: Several services use unstructured `log.Printf` calls. Structured JSON logging (e.g., via `slog` in Go 1.21+) would make CloudWatch Logs queries much more powerful and enable log-based Grafana dashboards.

5. **Terraform remote state**: The current setup stores Terraform state locally, which is problematic when both the AI agent (on ECS) and the local monitor need to run `terraform apply`. A remote backend (S3 + DynamoDB locking) would prevent state corruption from concurrent applies.

---

## Appendix A: Repository Structure

```
final_project/
├── api.yaml                              # OpenAPI spec (reference)
├── IMPLEMENTATION_PLAN.md                # Architecture and implementation plan
├── PROJECT_STATUS.md                     # Current deployment status
├── REPORT.md                             # This report
├── Makefile                              # Common commands
├── docker-compose.yml                    # Full local stack (Kafka + services + monitoring)
├── docker-compose.grafana.yml            # Local Grafana + Prometheus for remote monitoring
│
├── services/
│   ├── cart-api/                          # Shopping Cart API (Go) -- Producer
│   │   ├── main.go, handler.go, kafka.go, metrics.go, Dockerfile, go.mod
│   ├── analytics/                        # Analytics Service (Go) -- Consumer/Aggregator
│   │   ├── main.go, consumer.go, aggregator.go, producer.go, dynamo.go, metrics.go, Dockerfile, go.mod
│   ├── alert/                            # Alert Service (Go) -- Consumer/Evaluator
│   │   ├── main.go, consumer.go, evaluator.go, notifier.go, metrics.go, Dockerfile, go.mod
│   ├── ai-agent/                         # AI DevOps Agent (Go) -- Claude API approach
│   │   ├── main.go, consumer.go, claude.go, terraform.go, producer.go, metrics.go, Dockerfile, go.mod
│   └── monitor/                          # Local Monitor (Go) -- Claude Code approach
│       └── main.go
│
├── terraform/
│   ├── main.tf                           # VPC, ALB, ECS, Kafka, ECR, DynamoDB, SNS, auto-scaling
│   ├── variables.tf                      # All input variables
│   ├── outputs.tf                        # ALB DNS, ECR URLs, Prometheus URL
│   └── terraform.tfvars                  # Current parameter values
│
├── monitoring/
│   ├── prometheus/
│   │   ├── prometheus.yml                # Local dev scrape config
│   │   ├── prometheus-local-agent.yml    # Local Prometheus for remote + agent scraping
│   │   └── Dockerfile                    # Custom Prometheus image for ECS
│   └── grafana/
│       ├── datasources.yml, datasources-remote.yml, dashboard-provider.yml
│       └── dashboards/system-overview.json
│
├── loadtest/
│   └── locustfile.py                     # 4 load profiles: Steady, Ramp, Spike, Step
│
└── scripts/
    ├── deploy.sh                         # Build, push, deploy pipeline
    └── setup-kafka-topics.sh             # Idempotent topic creation
```

## Appendix B: Kafka Topic Configuration

| Topic | Partitions | Replication Factor (local) | Replication Factor (ECS) | Producers | Consumers |
|-------|-----------|---------------------------|--------------------------|-----------|-----------|
| `metrics-topic` | 6 | 3 | 1 | cart-api (N instances) | analytics (`analytics-group`), alert (`alert-group`) |
| `analytics-output-topic` | 3 | 3 | 1 | analytics | ai-agent (`ai-agent-group`) |
| `infra-events-topic` | 1 | 3 | 1 | ai-agent | (Grafana/audit) |

## Appendix C: Environment Variables

### cart-api

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP server port |
| `KAFKA_BROKERS` | `kafka-1:9092,kafka-2:9092,kafka-3:9092` | Comma-separated broker addresses |
| `KAFKA_TOPIC` | `metrics-topic` | Kafka topic for metric events |
| `INSTANCE_ID` | hostname | Unique instance identifier |

### analytics

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8081` | HTTP server port |
| `KAFKA_BROKERS` | `kafka-1:9092,kafka-2:9092,kafka-3:9092` | Comma-separated broker addresses |
| `KAFKA_CONSUMER_TOPIC` | `metrics-topic` | Input topic |
| `KAFKA_PRODUCER_TOPIC` | `analytics-output-topic` | Output topic |
| `CONSUMER_GROUP` | `analytics-group` | Kafka consumer group |
| `DYNAMODB_TABLE` | (empty) | DynamoDB table for persistence |
| `AWS_REGION` | (none) | AWS region for DynamoDB |

### alert

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8082` | HTTP server port |
| `KAFKA_BROKERS` | `localhost:9092` | Comma-separated broker addresses |
| `KAFKA_TOPIC` | `metrics-topic` | Input topic |
| `CONSUMER_GROUP` | `alert-group` | Kafka consumer group |
| `SNS_TOPIC_ARN` | (empty) | SNS topic ARN for email alerts |
| `AWS_REGION` | (none) | AWS region for SNS |

### ai-agent

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8083` | HTTP server port |
| `KAFKA_BROKERS` | `localhost:9092` | Comma-separated broker addresses |
| `KAFKA_CONSUMER_TOPIC` | `analytics-output-topic` | Input topic |
| `KAFKA_PRODUCER_TOPIC` | `infra-events-topic` | Output topic |
| `CONSUMER_GROUP` | `ai-agent-group` | Kafka consumer group |
| `ANTHROPIC_API_KEY` | (required) | Claude API key |
| `TERRAFORM_DIR` | `/terraform` | Path to Terraform directory |
| `DRY_RUN` | `true` | If true, log decisions without applying |
| `COOLDOWN_SECONDS` | `60` | Minimum seconds between applies |

### monitor (local)

| Flag | Default | Description |
|------|---------|-------------|
| `--table` | `cs6650-final-metrics` | DynamoDB table name |
| `--terraform-dir` | (auto-detected) | Path to terraform directory |
| `--cooldown` | `120` | Seconds between Claude Code invocations |
| `--poll` | `10` | Seconds between DynamoDB polls |
| `--dry-run` | `false` | Print prompt without invoking Claude Code |
