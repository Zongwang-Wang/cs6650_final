# Project Management — Album Store Monitor
## CS 6650 Final · Team: Zongwang Wang, Dylan, Lucas, Shiyue

---

## Team & Ownership

| Member | Component | Experiments |
|--------|-----------|-------------|
| **Dylan** | Kafka pipeline (broker, topics, producers on album store nodes) | Exp 1 — Kafka Throughput, Exp 2 — Cache Observability |
| **Lucas** | Analytics microservice (Kafka consumer → 60s aggregator → Prometheus) | Exp 4 — Monitoring Overhead |
| **Shiyue** | Alert service (Kafka consumer → SNS), DevOps Chat AI UI | Exp 3 — Alert Detection Speed |
| **Zongwang** | Grafana dashboards, Prometheus config, YACE/exporters, deployment infra | Exp 5 — Ramp vs Spike |

---

## Problem Decomposition

The core problem: **you can't improve what you can't see.** Our album store hit ChaosArena with no visibility — latency spikes, cache cold-starts, S3 bandwidth bottlenecks were all invisible. We broke the observability problem into four independent layers:

```
┌─────────────────────────────────────────────────────────────────────┐
│  LAYER 4 — VISUALIZATION (Zongwang)                                 │
│  Grafana dashboards, Prometheus, YACE CloudWatch exporter           │
├─────────────────────────────────────────────────────────────────────┤
│  LAYER 3 — ALERTING + AI (Shiyue)                                   │
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

**Why the design expanded:** During initial benchmarking we realized Prometheus scraping at 15s intervals was too coarse to catch burst behavior (S12, S15). We needed per-request event streaming to detect threshold breaches within seconds, not minutes. That's when Dylan proposed adding Kafka as a push-based pipeline alongside the pull-based Prometheus scraping.

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

### Shiyue — Alert Service + DevOps Chat

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

## Phase 3 — Integration & Issues

### Issue Log

| # | Issue | Who Hit It | Root Cause | Resolution |
|---|-------|-----------|------------|------------|
| 1 | `deploy-all.sh` fails at SSM step | Zongwang | AWS CLI region defaulted to `us-east-2`, not `us-west-2` | `export AWS_DEFAULT_REGION=us-west-2` before every deploy |
| 2 | `aws ssm send-command` returns exit 255 "badly formed help string" | Zongwang | Ubuntu 26.04 uses Python 3.14 which broke `bcdoc` string formatting in Ubuntu-packaged AWS CLI v2 | Replaced with official AWS CLI binary (bundles its own Python 3.12) |
| 3 | Analytics/alert Docker build fails: `/alert not found` | Lucas, Shiyue | Dockerfiles expect pre-built binaries; deploy script only shipped source | Added `CGO_ENABLED=0 go build` step before tarball packing |
| 4 | Prometheus `album_store` targets all `down` | Zongwang | `prometheus.yml` had hardcoded IPs from a previous deployment | Update node IPs after every `terraform apply`; now documented in deploy checklist |
| 5 | Request Rate panel always shows 0 | Zongwang | `rate(CloudWatch_Sum_metric[1m])` returns 0 — CloudWatch Sum metrics are per-period values (not cumulative counters), so `rate()` is invalid | Changed all CloudWatch Sum panels to `metric / 60` for req/s |
| 6 | EC2 CPU and Network panels always 0 | Zongwang | EC2 basic monitoring has 5-minute granularity; YACE requests 60s periods → CloudWatch returns no data | Enabled detailed monitoring: `aws ec2 monitor-instances`; added `monitoring = true` to Terraform |
| 7 | DevOps Chat shows raw `<iframe>` HTML | Shiyue | Grafana 10 sanitizes HTML in Text panels by default | Added `GF_PANELS_DISABLE_SANITIZE_HTML=true` to docker-compose |
| 8 | Application panels appeared under S3 row | Zongwang | Both rows had same `gridPos.y=42` in dashboard JSON — S3 row rendered first and consumed all subsequent panels | Fixed: S3 row moved to y=57, Application panels stay at y=43–56 |
| 9 | `terraform destroy` hangs 15 min then fails on security group | Zongwang | `deploy-all.sh` adds cross-SG ingress rules (monitor SG → album-store SG) that must be manually revoked before destroy | Added manual SG cleanup step to teardown runbook |
| 10 | Seq Keys panel shows no data | Zongwang | Panel queried `redis_key_group_count{key_group="album:*:seq"}` but redis_exporter exports the metric without `key_group` label | Changed panel to use `album_store_seq_incr_total` from Go server metrics |
| 11 | S3 no data initially | Zongwang | No objects in bucket (no photo uploads yet); s3-exporter needs objects to count | Not a bug — loads after first photo upload during load test |

---

## Phase 4 — Experiments

Each experiment validated a specific claim about the monitoring system itself.

### Exp 1 — Kafka Pipeline Throughput *(Dylan)*
**Question:** Does Kafka become a bottleneck under increasing load?
**Method:** Ran 4 load levels (10/30/60/100 users), measured req/s and latency, checked for Kafka consumer lag.
**Result:** 0 failures across all loads. p50 moved only 4ms across 7× throughput increase. Kafka is not the bottleneck.

### Exp 2 — Cache Observability *(Dylan)*
**Question:** Does the Redis Hit Rate panel actually reflect real cache behavior?
**Method:** Cold cache run vs warm cache run, same load, compare Grafana panel vs actual latency difference.
**Result:** Grafana showed hit rate climbing from 20% → 99% in real time. p95 8% lower warm vs cold. Panel validated.

### Exp 3 — Alert Detection Speed *(Shiyue)*
**Question:** How quickly does the alert service detect a threshold breach?
**Method:** Low load (below threshold) vs high load (above threshold). Measured time from breach to SNS delivery.
**Result:** Alert fired within one 10-second evaluation window. Faster than Prometheus 15s scrape interval.

### Exp 4 — Monitoring Overhead *(Lucas)*
**Question:** Does the Kafka producer + Prometheus instrumentation add latency to HTTP responses?
**Method:** Baseline (2 req/s) vs medium load (56 req/s), measured p95 with vs without Kafka events.
**Result:** Kafka producer is non-blocking channel write (~1μs). Prometheus counters ~1μs. Net overhead: negligible.

### Exp 5 — Ramp vs Spike Traffic Patterns *(Zongwang)*
**Question:** Can the monitoring stack distinguish a gradual ramp from an instantaneous spike?
**Method:** Ramp (5→60 users over 3 min) vs spike (0→80 users instantly). Compare goroutine semaphore graphs.
**Result:** Spike p95 4× higher than ramp despite only 33% more peak users. Goroutine semaphore chart shows vertical wall (spike) vs smooth slope (ramp) — visually distinct in Grafana.

---

## Final State

### What Was Built

```
┌──────────────────────────────────────────────────────────────────────┐
│  AWS INFRASTRUCTURE (Terraform-managed)                              │
│  4× EC2 c5n.large + ALB + RDS PostgreSQL + ElastiCache Redis + S3   │
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
          │ Analytics  │  │    Alert     │  ← Shiyue
          │ (Lucas)    │  │  → SNS email │
          │ /metrics   │  └──────────────┘
          └─────┬──────┘
                │ (+ 6 other exporters)
         ┌──────▼───────┐
         │  Prometheus  │
         └──────┬───────┘
                │
         ┌──────▼───────┐    ┌──────────────┐
         │   Grafana    │    │ DevOps Chat  │  ← Shiyue + Zongwang
         │  (Zongwang)  │    │  (AI iframe) │
         └──────────────┘    └──────────────┘
```

### Scores
- **Album Store (ChaosArena):** 190/190 — Rank 12/73
- **Monitoring system:** 11/11 Prometheus targets, <10s alert detection, ~1μs overhead

### Files
| File | Owner | Purpose |
|------|-------|---------|
| `services/analytics/` | Lucas | Analytics microservice |
| `services/alert/` | Shiyue | Alert microservice |
| `services/devops-chat/` | Shiyue | DevOps Chat UI |
| `prometheus/prometheus.yml` | Zongwang | Scrape config |
| `prometheus/cloudwatch.yml` | Zongwang | YACE CloudWatch config |
| `grafana/dashboards/` | Zongwang | All dashboard JSON |
| `docker-compose.yml` | Zongwang | Monitor stack orchestration |
| `deploy-all.sh` | Zongwang | One-command deployment |
| `final_mastery/terraform/` | Zongwang | AWS infrastructure |
| `final_mastery/server/` | Zongwang | Album store server (instrumented) |
| `loadtest/` | All | Locust load test files |
| `experiments/` | All | Experiment scripts and data |
| `notifications.json` | Shiyue | Alert thresholds + team emails |
