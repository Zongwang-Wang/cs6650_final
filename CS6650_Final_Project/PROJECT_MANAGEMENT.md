# Project Management — Album Store Monitor
## CS 6650 Final · Team: Zongwang Wang · Dylan Pan · Lucas Chen · Ellis Guo  

---

## Team & Ownership

| Member | Component | Experiments |
|--------|-----------|-------------|
| **Dylan** | Kafka pipeline (broker, topics, producers on album store nodes) | Exp 1 — Kafka Pipeline Throughput, Exp 2 — Alert Detection Latency |
| **Ellis** | Alert service (Kafka consumer → SNS), DevOps Chat AI UI | Exp 3 — Redis Cache Effectiveness, Exp 4 — Monitoring Pipeline Latency Budget |
| **Lucas** | Analytics microservice (Kafka consumer → 60s aggregator → Prometheus) | Exp 5 — Prometheus Observability Coverage, Exp 6 — Alert Threshold Correctness |
| **Zongwang** | Grafana dashboards, Prometheus config, YACE/exporters, deployment infra | Report summary & conclusion |

---

## Problem Decomposition

The core problem: **you can't improve what you can't see.** Our album store hit ChaosArena with no visibility — latency spikes, cache cold-starts, S3 bandwidth bottlenecks were all invisible. We broke the observability problem into four independent layers:

```
┌─────────────────────────────────────────────────────────────────────┐
│  LAYER 4 — VISUALIZATION (Zongwang)                                 │
│  Grafana dashboards, Prometheus, YACE CloudWatch exporter           │
├─────────────────────────────────────────────────────────────────────┤
│  LAYER 3 — ALERTING + AI (Ellis)                                    │
│  Alert service (SNS email), DevOps Chat iframe                      │
├─────────────────────────────────────────────────────────────────────┤
│  LAYER 2 — ANALYTICS (Lucas)                                        │
│  Analytics microservice: 60s window aggregation → /metrics          │
├─────────────────────────────────────────────────────────────────────┤
│  LAYER 1 — DATA PIPELINE (Dylan)                                    │
│  Kafka broker (KRaft), topics, producer in album store handler      │
└─────────────────────────────────────────────────────────────────────┘
              ▲ all layers feed into Grafana
```

Each layer is independent: Kafka doesn't need Grafana to work; alerting doesn't need analytics. This made parallel development possible and failures in one layer didn't block others.

---

## Phase 1 — Initial Design

**Goal:** Single instrumented server with basic Prometheus metrics.

**Initial architecture (what we planned):**

```
Album Store nodes ──► Prometheus /metrics ──► Grafana
```

Simple pull-based scraping only. No Kafka, no alerting, no AI.

**What we actually built instead:**

```
Album Store nodes ──► Kafka ──► Analytics  ──► Prometheus ──► Grafana
                        │                                         ▲
                        └────► Alert ──► SNS email               │
                                                    DevOps Chat ──┘
```

**Why the design expanded:** During initial benchmarking we realized Prometheus scraping at 15s intervals was too coarse to catch burst behavior. We needed per-request event streaming to detect threshold breaches within seconds, not minutes. That's when Dylan proposed adding Kafka as a push-based pipeline alongside the pull-based Prometheus scraping.

---

## Phase 2 — Parallel Development

### Dylan — Kafka Pipeline

**Design decision:** Single-broker KRaft (no ZooKeeper). Three topics:
- `album-metrics` (6 partitions) — per-request events from all 4 nodes
- `album-analytics-output` (3 partitions) — aggregated window output
- `album-infra-events` (1 partition) — infra lifecycle events

**Instrumentation in album store handler:**
```go
// Non-blocking producer — does not add latency to HTTP response path
producer.WriteMessages(ctx, kafka.Message{
    Key:   []byte(instanceID),
    Value: json.Marshal(RequestEvent{Method, Path, StatusCode, LatencyMS}),
})
```

**Problem encountered:** Kafka broker needed `KAFKA_ADVERTISED_LISTENERS` set to the monitor instance's *private* IP, not `localhost`, so that the album store nodes (on different EC2s) could reach the broker. First version used `localhost` — analytics and alert consumed fine (same Docker network) but album store nodes couldn't connect.
- **Fix:** Deploy step 6 injects `KAFKA_BROKERS=<MONITOR_PRIV_IP>:9094` into `/etc/album-store.env` on each node and restarts the service.

---

### Lucas — Analytics Microservice

**Design:** Kafka consumer group `analytics-group`, reads `album-metrics`, maintains a rolling 60-second window per instance, publishes aggregated stats to `/metrics` on port 8081.

**Metrics exposed:**
- `analytics_window_requests_total` — req count per 60s window per node
- `analytics_window_latency_p95_ms` — p95 across the window
- `analytics_window_error_rate` — 5xx fraction

**Problem encountered:** The analytics service was built with Go 1.24 (newer AWS SDK dependency) while the rest of the project used Go 1.23. The Dockerfile used `FROM alpine:3.19` and expected a pre-compiled binary (`COPY analytics .`), but the deploy tarball didn't include built binaries — only source code.
- **Fix:** Added explicit `go build` step before packing the deployment tarball:
  ```bash
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o analytics .
  ```

---

### Ellis — Alert Service + DevOps Chat

**Alert service design:** Kafka consumer group `alert-group`, evaluates per-instance p95 every 10 seconds against thresholds in `notifications.json`. Fires SNS email if breached.

```json
{
  "thresholds": {
    "cpu_alert_pct": 10,
    "latency_p99_ms": 500,
    "error_rate_pct": 5,
    "cache_hit_min_pct": 50
  }
}
```

**SNS targets:** All 4 team members' Northeastern emails.

**DevOps Chat:** Python HTTP server embedding a Grafana iframe + Claude API integration. Accepts natural-language queries, pulls live Prometheus metrics, returns AI analysis.

**Problem encountered (alert service):** Same binary-not-included problem as analytics — `Dockerfile` expected `COPY alert .` but binary wasn't in the tarball.
- **Fix:** Same as analytics — explicit build step in deploy sequence.

**Problem encountered (DevOps Chat iframe):** Grafana sanitizes `<iframe>` tags in Text panels by default in v10+. Panel rendered raw HTML instead of the iframe.
- **Fix:** Added `GF_PANELS_DISABLE_SANITIZE_HTML=true` to docker-compose Grafana environment.

---

### Zongwang — Dashboard + Deployment Infrastructure

**Grafana dashboard:** 7 sections, 25 panels covering ALB, Redis, RDS, EC2, Application /metrics, S3, DevOps Chat.

**Deployment:** `deploy-all.sh` orchestrates the full stack:
1. Terraform → provision RDS, ElastiCache, EC2×4, ALB, S3
2. Go build → compile album store server binary
3. S3 upload → binary to S3
4. SSM → pull binary, restart systemd service on all 4 nodes
5. EC2 launch → t3.small monitor instance
6. SSM → inject Kafka broker IP into album store env
7. SSM → build Docker images, `docker compose up -d`

---

## Phase 3 — Experiments

Each experiment validated a specific claim about the monitoring system itself.

### Exp 1 — Kafka Pipeline Throughput *(Dylan)*
**Question:** Does Kafka become a bottleneck under increasing load?
**Method:** 4 load levels (10/30/60/100 users), measured req/s and p50/p95 latency.
**Result:** 0 failures across all loads. p50 moved only 4ms across 7× throughput increase. Kafka is not the bottleneck.

### Exp 2 — Alert Detection Latency *(Dylan)*
**Question:** How quickly does the alert service detect a threshold breach per instance?
**Method:** Applied burst load forcing sustained p99 > 500ms across all 4 nodes. Measured time from load start to SNS delivery per instance.
**Result:** All 4 nodes alerted independently within ~40s. Per-instance evaluation confirmed.

### Exp 3 — Redis Cache Effectiveness *(Ellis)*
**Question:** Does the Redis cache layer actually reduce database load and improve tail latency?
**Method:** Warm cache run (known album IDs) vs cold simulation (fresh UUID IDs), 30 users, 60s each.
**Result:** Warm hit rate 97.2% vs cold 82.1%. p95 increased 9% under cold conditions. p50 unchanged — cache value is DB protection, not median latency.

### Exp 4 — Monitoring Pipeline Latency Budget *(Ellis)*
**Question:** How quickly does a service change become visible on the dashboard vs. trigger an SNS alert?
**Method:** Traced each path through its collection pipeline, summing measured and design-constant delays.
**Result:** Dashboard path ~22s (analytics 9s + Prometheus 13s). Alert path ~36s (30s window + 6s SNS). Kafka infrastructure contributes <100ms in both paths.

### Exp 5 — Prometheus Observability Coverage *(Lucas)*
**Question:** Are all monitoring data sources active and contributing distinct metrics?
**Method:** Queried `/api/v1/targets` during live load, enumerated metric families per source.
**Result:** 11/11 targets up, ~100 distinct metrics. Zero overlap between sources.

### Exp 6 — Alert Threshold Correctness *(Lucas)*
**Question:** Does the alert service fire on true positives and suppress false positives?
**Method:** Low load (5 users) vs high load (40 users). Counted alerts after each run.
**Result:** 0 alerts at low load (no false positives). 4 alerts at high load (1 per node). 30s window suppresses transient spikes.

---

## Final State

### What Was Built

```
┌──────────────────────────────────────────────────────────────────────┐
│  AWS INFRASTRUCTURE (Terraform-managed)                              │
│  4× EC2 c5n.large + ALB + RDS PostgreSQL + ElastiCache Redis + S3    │
└──────────────────────────┬───────────────────────────────────────────┘
                           │ HTTP (instrumented)
                ┌──────────▼──────────┐
                │  Album Store Server  │  ← Go, 4 nodes
                │  /metrics endpoint  │  ← Prometheus pull
                │  Kafka producer     │  ← push per-request
                └──────────┬──────────┘
                           │ album-metrics topic
             ┌─────────────▼─────────────┐
             │       Kafka Broker        │  ← KRaft, t3.small
             └──────┬─────────┬──────────┘
                    │         │
          ┌─────────▼──┐  ┌───▼──────────┐
          │ Analytics  │  │    Alert     │  ← Ellis
          │ (Lucas)    │  │  → SNS email │
          │ /metrics   │  └──────────────┘
          └─────┬──────┘
                │ (+ 6 other exporters)
         ┌──────▼───────┐
         │  Prometheus  │
         └──────┬───────┘
                │
         ┌──────▼───────┐    ┌──────────────┐
         │   Grafana    │    │ DevOps Chat  │  ← Ellis + Zongwang
         │  (Zongwang)  │    │  (AI iframe) │
         └──────────────┘    └──────────────┘
```



