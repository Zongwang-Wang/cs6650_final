# PPT Slide Bullets
## Album Store Monitor — CS 6650 Final

---

## SLIDE: Title / Intro

- **Problem:** Distributed backend systems fail silently — latency spikes, cache cold-starts, S3 bottlenecks are invisible without instrumentation
- **Our tool:** Real-time observability stack for distributed backend servers
- **Case study:** Applied to album store submitted to ChaosArena — unpredictable load, unknown scenarios, graded on p95 latency
- **Result:** 190/190 · Rank 12/73

---

## SLIDE: Architecture Overview

- **Dual telemetry channels** — pull-based Prometheus (15s scrape) + push-based Kafka (per-request event stream)
- **4 album store nodes** instrument every HTTP request → non-blocking Kafka producer (~1μs overhead)
- **Kafka broker** (KRaft, single-node) → fans out to two consumer microservices
- **Analytics service** (Lucas) — 60s window aggregation → exposes `/metrics` to Prometheus
- **Alert service** (Ellis) — 10s threshold evaluation → SNS email to all 4 team members
- **Prometheus** scrapes 11 targets: 4 nodes + analytics + alert + Redis exporter + Postgres exporter + YACE (CloudWatch) + S3 exporter
- **Grafana** — single dashboard, 7 sections, 25 panels
- **DevOps Chat** (Ellis) — AI assistant embedded in Grafana, queries live Prometheus, natural-language analysis

```
Album Store ×4 → Kafka → Analytics → Prometheus → Grafana
                    ↓                                  ↑
                  Alert → SNS Email     DevOps Chat ──┘
```

---

## SLIDE: Dashboard — HTTP / ALB

- **Request Rate (req/s)** — live traffic from CloudWatch ALB; detects load spikes and unexpected traffic drops
- **5xx Error Rate (%)** — zero tolerance; any non-zero value = requests failing under load
- **P95 Latency (ms)** — the metric ChaosArena grades on; early warning before SLA breach
- **Healthy Nodes** — catches silent node failure; losing 1 of 4 nodes = 25% capacity drop with no HTTP errors
- *Why it matters:* ChaosArena scores purely on p95 — this panel is the grader's view of your system in real time

---

## SLIDE: Dashboard — Redis / ElastiCache

- **Cache Hit Rate (%)** — album GET hits ~99% after warm-up; drop here explains latency spikes without touching the DB
- **Cache Hits vs Misses (rate/s)** — separates album reads from photo reads; shows which operation is hitting Redis vs falling to PostgreSQL
- **Seq INCRs Total** — cumulative Redis `INCR` calls across all 4 nodes; confirms atomic sequence counter is working globally
- **Redis Memory Used** — eviction risk indicator; if memory fills, seq counters or cache entries start dropping
- *Why it matters:* All 4 nodes share one Redis — a shared atomic INCR counter is why seq numbers never duplicate under 256 concurrent uploads

---

## SLIDE: Dashboard — RDS PostgreSQL

- **DB Connections (active vs max)** — 4 nodes × 25 connections = 100/170 max; saturation = queuing = latency spike
- **DB Operations/s** — INSERT vs SELECT ratio reveals workload type (write-heavy during uploads, read-heavy during GETs)
- **RDS CPU / IOPS** — headroom indicators; above 80% CPU = need to scale instance class
- *Why it matters:* S11 (concurrent album creates) stresses the DB most — this panel shows whether the DB is the bottleneck

---

## SLIDE: Dashboard — Application /metrics (per node)

- **Redis Hit Rate by Operation** — per-node breakdown; identifies cold nodes or uneven cache warming across the cluster
- **Active Goroutine Semaphore Slots** — bounded concurrency: 64 slots for small uploads, 8 for large (per node)
  - Pinned at max → bandwidth saturated → scale out
  - Near zero → idle capacity
  - Spike then recover → burst, system absorbed it
- **S3 Upload Duration (p50/p95/p99)** — measures the background goroutine, not the HTTP response; detects S3 slowness before ALB latency reacts
- *ChaosArena example (S15):* Large semaphore pinned at 8/node (32 total) → each got ~3 Gbps → 200 MB uploaded in <6s

---

## SLIDE: Dashboard — S3 + DevOps Chat

- **Total Objects / Total Bucket Size** — confirms background uploads are completing; silent upload failures show here first
- **S3 Upload Duration** — end-to-end goroutine timing; if this climbs, ALB p95 will follow in ~30s
- **DevOps Chat** — embedded AI assistant; queries live Prometheus metrics on demand
  - Ask: *"why is p95 high right now?"* → pulls metrics, returns root cause analysis
  - Useful during live incidents when you need interpretation, not just numbers

---

## SLIDE: Experiment 1 — Kafka Pipeline Throughput *(Dylan)*

- **Question:** Does Kafka become a bottleneck as load scales?
- **Method:** 4 load levels — 10, 30, 60, 100 concurrent users

| Users | req/s | p50 | p95 |
|-------|-------|-----|-----|
| 10 | 32 | 15ms | 190ms |
| 30 | 94 | 16ms | 350ms |
| 60 | 164 | 18ms | 670ms |
| 100 | 219 | 19ms | 710ms |

- **Result:** 0 failures at all loads; p50 moved only 4ms across 7× throughput increase
- **Takeaway:** Kafka producer is non-blocking — it does not distort the metrics it collects

---

## SLIDE: Experiment 2 — Cache Observability *(Dylan)*

- **Question:** Does the Grafana hit rate panel reflect actual cache behavior, or is it noise?
- **Method:** Same load (83 req/s), cold cache vs warm cache

| Scenario | p95 | Hit rate |
|----------|-----|----------|
| Cold cache | 820ms | 20% → climbs |
| Warm cache | 750ms | 99% immediately |

- **Result:** p95 8% lower warm vs cold; Grafana showed hit rate climbing in real time from 20% → 99% over first 30s
- **Takeaway:** Panel validated — it measures real behavior, not a phantom metric

---

## SLIDE: Experiment 3 — Alert Detection Speed *(Ellis)*

- **Question:** How quickly does the alert service detect a threshold breach?
- **Method:** Low load (18 req/s, below threshold) vs high load (135 req/s, above threshold)

| Load | req/s | p95 | Alert fired? |
|------|-------|-----|-------------|
| Low | 18 | 650ms | No |
| High | 135 | 760ms | **Yes — within 10s** |

- **Result:** Breach detected and SNS email sent within one 10-second evaluation window
- **Takeaway:** Push-based Kafka pipeline enables faster alerting than pull-based Prometheus scraping (15s vs 10s)

---

## SLIDE: Experiment 4 — Monitoring Overhead *(Lucas)*

- **Question:** Does adding Kafka + Prometheus instrumentation slow down the server?
- **Method:** Measured latency with full instrumentation at baseline and medium load

| Load | req/s | p95 |
|------|-------|-----|
| Baseline | 2.1 | 530ms |
| Medium | 56 | 720ms |

- **Kafka producer overhead:** ~1μs per request (non-blocking channel write)
- **Prometheus counter overhead:** ~1μs per request (atomic increment)
- **Result:** No measurable degradation attributable to monitoring
- **Takeaway:** Monitoring a system should not degrade the system — ours doesn't

---

## SLIDE: Experiment 5 — Ramp vs Spike Patterns *(Zongwang)*

- **Question:** Can the monitoring stack distinguish gradual ramp from instantaneous spike?
- **Method:**
  - Ramp: 5 → 60 users over 3 minutes
  - Spike: 0 → 80 users instantly

| Pattern | req/s | p50 | p95 | Semaphore chart |
|---------|-------|-----|-----|----------------|
| Ramp | 148 | 20ms | 260ms | Smooth slope |
| Spike | 119 | 18ms | 1,100ms | Vertical wall |

- **Result:** Spike p95 is **4× higher** despite only 33% more peak users
- **Takeaway:** Same peak load, very different impact — Grafana makes the difference visually obvious
  - Vertical wall → need a **rate limiter**
  - Smooth plateau at max → need **more capacity**

---

## SLIDE: Key Takeaways

- **Two channels, two granularities:** Prometheus (15s scrape) for trends; Kafka (per-event) for real-time alerting
- **Three visibility layers:** Infrastructure (CloudWatch) → Application (Go /metrics) → Pipeline (Kafka analytics)
- **Not plug-and-play — intentionally customizable:** You choose what to instrument; the framework handles collection, storage, and visualization
- **Overhead is negligible:** ~1μs per request; monitoring does not degrade the system it observes
- **Alert speed:** Threshold breach detected in <10 seconds — faster than the grader's next request
- **Final result:** 190/190 on ChaosArena, Rank 12/73 — visibility enabled the tuning that got there
