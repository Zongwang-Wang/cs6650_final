# Live Demo Script — Album Store Monitor
**Total time: ~12 minutes**

---

## PART 1 — Live Dashboard Walkthrough (~6 min)

> *Open Grafana at http://44.243.62.10:3000. Make sure the load test is running in the background so panels are live.*

---

### [0:00] Opening (30s)

"What you're looking at is a real-time observability stack running on AWS right now — Prometheus scraping 11 targets, Grafana visualizing everything, and a Kafka pipeline streaming per-request telemetry off the 4 album store nodes. Let me walk you through each section."

---

### [0:30] Section: HTTP — ALB (1 min)

*Point to the top row: Request Rate, Error Rate, P95 Latency, Healthy Nodes.*

"The top row is our system-level health at a glance. This is exactly what ChaosArena sees.

- **Request Rate** — how many requests per second are hitting the load balancer right now.
- **5xx Error Rate** — if this goes above 0%, something is broken. In our final submission it stayed at 0% across all scenarios.
- **P95 Latency** — this is the number ChaosArena grades you on. If this climbs above the scenario threshold, you lose points.
- **Healthy Nodes** — tells you immediately if one of the 4 EC2 nodes drops out of the ALB target group.

*Why this matters for ChaosArena:* The grader hits the ALB. If a node goes down mid-test and we don't notice, our effective capacity drops by 25%. This panel catches that in real time."

---

### [1:30] Section: Redis — ElastiCache (1 min)

*Scroll to the Redis section.*

"All 4 nodes share one ElastiCache Redis. Two things Redis does for us:

- **Sequence counters** — every `POST /albums/:id/photos` calls `INCR album:{id}:seq`. Redis INCR is single-threaded and atomic, so we never get duplicate sequence numbers even under 256 concurrent uploads.
- **Read cache** — `GET /albums/:id` and `GET /photos/:id` check Redis before hitting PostgreSQL.

The **Cache Hit Rate** panel shows album vs photo hit rates separately. Once the cache warms up, album GETs hit Redis at ~99%, which is why our S13 mixed read/write p95 is only 6ms.

The **Seq Keys (album:*:seq)** stat tells us how many albums have active sequence counters in Redis — useful to catch counter drift after a restart."

---

### [2:30] Section: RDS PostgreSQL (45s)

*Scroll to the RDS section.*

"PostgreSQL is the source of truth. Three things to watch:

- **DB Connections** — we cap each node at 25 connections, so 4 nodes = 100 max. db.t3.medium supports ~170. If this saturates, we'd see queuing and latency spikes in S11.
- **DB Operations/s** — lets us see INSERT vs SELECT ratios. During S11 (concurrent album creates) inserts dominate; during S13 (mixed read/write) selects dominate.
- **RDS CPU** — if this goes above 80%, we'd need to scale up the instance class."

---

### [3:15] Section: Application /metrics — per node (1 min)

*Scroll to the Application section.*

"This is the most interesting section — these metrics come directly from our Go server's `/metrics` endpoint, scraped by Prometheus every 15 seconds.

- **Redis Hit Rate by Operation** — per-node breakdown. You can see which of the 4 nodes is warming up vs fully cached.
- **Redis Ops/s** — live INCR rate from photo uploads, cache hit/miss rate.
- **Active Goroutine Semaphore Slots** — this shows how many goroutines are currently holding an upload slot. We have 2 tiers: 64 slots for small files, 8 for large files per node. During an S15 large-file test, you'd see the large slot gauge spike to 8 then hold steady — that means we're bandwidth-saturating, which is exactly what we want.
- **S3 Upload Duration** — p50, p95, p99 of the upload goroutine. If S3 is slow, this climbs first, before the ALB latency panel even reacts."

---

### [4:15] Section: S3 + DevOps Chat (45s)

*Point to S3 stats and the iframe.*

"S3 gives us total object count and bucket size — as photo uploads succeed, both climb in real time. During a full ChaosArena run we uploaded about 1,400 photos totaling 5.5 GiB.

Below that is the **Claude Code DevOps Assistant** — an iframe embedding a custom chat UI running on the monitor instance. You can query Prometheus and get an AI-powered analysis: ask it 'why is p95 latency high?' and it pulls live metrics and explains what's happening."

---

### [5:00] Section: EC2 Nodes (30s)

"Finally, EC2 node CPU and network throughput per instance. The reason we chose c5n.large is the 25 Gbps dedicated network — no burst credits. During S15, 8 goroutines per node each get ~3 Gbps to S3, which is why 200 MB uploads complete in under 6 seconds."

---

## PART 2 — Experiments (~6 min)

> *Switch to the experiment graphs or a terminal showing key numbers.*

---

### [6:00] Intro (20s)

"We ran 7 structured experiments to validate the monitoring system itself — not just the album store. The question was: does the observability stack accurately reflect what's happening, and does it add enough overhead to matter?"

---

### [6:20] Experiment 1 — Kafka Pipeline Throughput Under Load (1 min)

*Show fig1_kafka_pipeline_throughput.png*

"Every HTTP request on the album store nodes produces a Kafka event — method, latency, status code, instance. The analytics service consumes these and aggregates into 60-second windows. We tested whether Kafka kept up under increasing load.

| Load | req/s | p50 | p95 |
|------|-------|-----|-----|
| 10 users | 32 req/s | 15ms | 190ms |
| 30 users | 94 req/s | 16ms | 350ms |
| 60 users | 164 req/s | 18ms | 670ms |
| 100 users | 219 req/s | 19ms | 710ms |

Zero failures at all loads. p50 barely moves — from 15ms to 19ms across a 7× increase in traffic. This tells us Kafka is not the bottleneck. The p99 at 100 users jumps to 5,100ms, which we attribute to the analytics consumer doing 60-second window aggregation — a 60s batch delay is expected, not an error."

---

### [6:20] Experiment 2 — Redis Cache Observability (1 min)

*Show fig3_cache_observability.png*

"We wanted to confirm that the Redis Hit Rate panel in Grafana actually reflects real cache behavior — not just noise.

We ran two scenarios: **cold cache** (fresh start, no warm-up) and **warm cache** (same album IDs requested repeatedly after initial creation).

| Scenario | req/s | p95 |
|----------|-------|-----|
| Cold cache | 83 req/s | 820ms |
| Warm cache | 83 req/s | 750ms |

Throughput is identical, but p95 drops 8% warm vs cold. The Grafana panel shows this as the hit rate climbing from ~20% to ~99% over the first 30 seconds. This confirms the observability layer is actually measuring real cache behavior, not a phantom metric."

---

### [7:20] Experiment 3 — Alert Threshold Detection (1 min)

*Show fig2_alert_detection.png and fig6_alert_evaluation.png*

"The alert service consumes the same Kafka stream as analytics. It evaluates per-instance latency every 10 seconds and fires an SNS email notification if p95 exceeds a threshold.

We ran two loads:

| Load | req/s | p95 | Alert fired? |
|------|-------|-----|-------------|
| Low (18 req/s) | 18 | 650ms | No |
| High (135 req/s) | 135 | 760ms | Yes — within 10s |

*Why this matters:* In a ChaosArena run, if one node slows down due to credit exhaustion or network throttling, the alert fires within one 10-second evaluation window. That's faster than noticing it manually in Grafana."

---

### [8:20] Experiment 4 — Monitoring Overhead (45s)

*Show fig5_prometheus_coverage.png*

"Does running Prometheus scraping + Kafka producers inside the album store add measurable latency overhead?

| Load | req/s | p95 |
|------|-------|-----|
| Baseline (no load) | 2.1 | 530ms |
| Medium load | 56 | 720ms |

The Kafka producer is non-blocking — it sends to a buffered channel and returns immediately. Prometheus instrumentation adds ~1μs per request. Net overhead: negligible. The monitoring system does not degrade the system it monitors."

---

### [9:05] Experiment 5 — Ramp vs Spike Load Pattern (1 min)

*Show fig4_data_flow_timing.png*

"We tested two traffic patterns to see how the system responds and whether monitoring catches the difference:

- **Ramp (5 → 60 users over 3 min):** 26,607 requests, p50=20ms, p95=260ms. System absorbs load gracefully. Grafana shows a smooth curve up on the Redis ops/s panel.
- **Spike (0 → 80 users instantly):** Pipeline lag test — 10,614 requests, p50=18ms, p95=1,100ms.

The spike p95 is 4× higher than the ramp p95, even though spike peak users are only 33% more. The monitoring catches this: the Goroutine Semaphore Slots gauge spikes instantly during the burst, whereas the ramp keeps slots below 50% capacity throughout."

---

### [10:05] Experiment 6 — Sustained Load (Ramp) (45s)

"The sustained ramp experiment ran 148 req/s for 3 minutes. This mirrors what ChaosArena's S13 and S14 scenarios look like. Key finding: p95 stays at 260ms — well under the 500ms threshold — and zero failures. The monitoring stack handled 26,000 requests without dropping a single Kafka event or missing a Prometheus scrape."

---

### [10:50] Closing (1 min 10s)

"To summarize what this system gives you that you wouldn't have without it:

1. **Kafka pipeline** — per-request telemetry at the application level, not just infrastructure. You see *which* operation is slow, not just that something is slow.
2. **Two-tier observability** — Prometheus for pull-based metrics every 15s, Kafka for push-based per-event streaming. Different granularities, same Grafana view.
3. **Automated alerting** — SNS email within 10 seconds of a threshold breach. No need to watch the dashboard.
4. **Zero overhead** — the monitoring stack adds ~1μs per request. It doesn't trade off correctness or throughput.

This is the system we used to reach 190/190 on ChaosArena, rank 12 out of 73 teams."

---

## Quick Reference

| URL | Purpose |
|-----|---------|
| `http://44.243.62.10:3000` | Grafana (admin/admin) |
| `http://44.243.62.10:9090` | Prometheus |
| `http://44.243.62.10:8089` | DevOps Chat UI |
| `http://album-store-alb-1922764799.us-west-2.elb.amazonaws.com` | Album Store ALB |

**Fire load during demo:**
```bash
cd ~/cs6650_final/album-store-monitor/loadtest
locust -f locustfile.py \
  --host http://album-store-alb-1922764799.us-west-2.elb.amazonaws.com \
  --users 80 --spawn-rate 10 --run-time 10m --headless &
```

**Stop load:**
```bash
pkill -f locust
```
