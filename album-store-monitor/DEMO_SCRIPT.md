# Demo Script — Album Store Monitor
**"Building Observability for Distributed Backend Systems"**
**Total time: ~15 minutes** *(adjust pacing to hit 12 if needed — experiments are droppable)*

---

## OPENING — The Problem (1 min) [0:00–1:00]

"Today we're talking about monitoring and observability — a problem every backend server faces, and one that becomes significantly harder in a distributed system. When you have multiple nodes, a shared database, a cache, and external storage, a failure can happen in any layer, or in the interaction between layers. Knowing what's happening — and why — requires intentional instrumentation.

The tool we built is a real-time observability stack for distributed backend servers. It is not plug-and-play. It's customizable — you decide what to measure, what thresholds matter, what to alert on. But once configured, it gives you a complete picture of your system from the infrastructure layer down to what's happening inside each server process.

We applied this tool to a concrete example: our album store, a distributed photo storage API that we submitted to ChaosArena. ChaosArena is a graded load testing platform — it sends unpredictable traffic patterns at your server and scores you on correctness and latency. You don't know what scenarios are coming, how many concurrent users, or how large the files will be. That's exactly like real life.

So this demo has two goals: show how the tool works panel by panel, and explain why each metric matters — using ChaosArena as the real-world example."

---

## ARCHITECTURE — How It All Connects (2 min) [1:00–3:00]

*Draw attention to or display the architecture diagram below.*

```
┌─────────────────────────────────────────────────────────────────────┐
│                        ALBUM STORE NODES (×4)                       │
│                                                                     │
│   HTTP Request ──► Handler ──► [Prometheus /metrics]                │
│                        │                                            │
│                        ▼                                            │
│               Kafka Producer (non-blocking)                         │
│         {method, path, latency_ms, status, instance_id}             │
└──────────────────────────┬──────────────────────────────────────────┘
                           │  album-metrics topic (6 partitions)
                           ▼
              ┌────────────────────────┐
              │       KAFKA BROKER     │  ← KRaft, single-broker
              │   (apache/kafka:3.8.1) │
              └────────┬───────────────┘
                       │
          ┌────────────┴─────────────┐
          │                          │
          ▼                          ▼
  ┌───────────────┐         ┌────────────────┐
  │   ANALYTICS   │         │     ALERT      │
  │   SERVICE     │         │    SERVICE     │
  │  (Go, :8081)  │         │  (Go, :8082)   │
  │               │         │                │
  │ 60s window    │         │ per-instance   │
  │ aggregation   │         │ p95 threshold  │
  │               │         │ evaluation     │
  │ → /metrics    │         │ → SNS email    │
  └──────┬────────┘         └────────────────┘
         │
         │  (all services expose /metrics)
         ▼
  ┌──────────────────────────────────────────┐
  │             PROMETHEUS (:9090)           │
  │                                          │
  │  Scrapes every 15s:                      │
  │  • album_store nodes ×4  (app metrics)   │
  │  • analytics, alert      (pipeline)      │
  │  • redis_exporter        (ElastiCache)   │
  │  • postgres_exporter     (RDS)           │
  │  • yace                  (CloudWatch)    │
  │  • s3-exporter           (S3 bucket)     │
  └──────────────────┬───────────────────────┘
                     │
                     ▼
  ┌──────────────────────────────────────────┐
  │              GRAFANA (:3000)             │
  │  + DevOps Chat UI (:8089)                │
  └──────────────────────────────────────────┘
```

"There are two telemetry channels running in parallel.

The first is **Kafka** — a push-based pipeline. Every HTTP request the album store handles emits an event to Kafka with the method, path, latency, status code, and which node handled it. Two microservices consume that stream:
- The **Analytics service** aggregates events into 60-second windows and exposes summary metrics.
- The **Alert service** evaluates per-instance p95 latency every 10 seconds and fires an SNS email if a threshold is breached.

The second channel is **Prometheus** — a pull-based scraper that hits eleven endpoints every 15 seconds. These include the album store's own `/metrics` endpoint, the two microservices, plus exporters for Redis, PostgreSQL, CloudWatch, and S3.

Everything flows into Grafana. The combination gives you two granularities: per-event streaming for real-time alerting, and time-series aggregates for trend analysis."

---

## LIVE DASHBOARD WALKTHROUGH (7 min) [3:00–10:00]

*Fire load before starting this section:*
```bash
locust -f loadtest/locustfile.py \
  --host http://album-store-alb-1922764799.us-west-2.elb.amazonaws.com \
  --users 80 --spawn-rate 10 --run-time 15m --headless &
```

---

### Panel 1 — HTTP / ALB: System-level health at a glance [3:00–4:15]

*Point to: Request Rate, 5xx Error Rate, P95 Latency, Healthy Nodes.*

"The top row answers the question your on-call engineer asks first: **is the system up, and is it fast?**

- **Request Rate** — how much traffic is hitting the load balancer. In general this tells you if load is growing, if a traffic spike is happening, or if traffic dropped suspiciously to zero.
- **5xx Error Rate** — the most critical signal. Any non-zero value here means requests are failing. In a well-behaved distributed system this should be zero even under high load.
- **P95 Latency** — the experience of the slowest 5% of your users. This is the metric most SLAs are defined against. If you see this climbing, you have a problem before your average latency even moves.
- **Healthy Nodes** — tells you immediately if a node dropped out of the load balancer. In a 4-node setup, losing one node means 25% less capacity with no visible error signal unless you're watching this.

*ChaosArena context:* ChaosArena grades entirely on p95 latency per scenario. This panel was our early-warning system — if p95 started climbing during a run, we knew to investigate before the grader locked in a score."

---

### Panel 2 — Redis / ElastiCache: Cache behavior and seq counters [4:15–5:30]

*Point to: Cache Hit Rate, Cache Hits vs Misses, Seq Keys, Redis Memory.*

"Redis in a distributed system typically does two things: caching and coordination. Both are visible here.

**On caching:** the Cache Hit Rate panel shows what percentage of read requests are served from memory vs falling through to the database. A high hit rate means low database load and fast responses. A sudden drop in hit rate — say after a deployment that flushes the cache — shows up immediately here and explains a latency spike that would otherwise look mysterious.

**On coordination:** this system uses Redis for globally atomic sequence numbers. Every time a photo is uploaded to any of the 4 nodes, it calls `INCR` on a shared Redis key. The 'Seq Keys' panel shows how many active counters are in Redis. This is important in any distributed system where you need a shared counter across instances — Redis INCR is the right tool and this panel confirms it's working.

*ChaosArena context:* Scenario S13 was a mixed read/write workload. After the cache warmed up in the first 30 seconds, hit rate climbed to 99% and p95 dropped to 6ms. This panel made that relationship visible in real time."

---

### Panel 3 — RDS PostgreSQL: Database pressure [5:30–6:15]

*Point to: DB Connections, DB Operations/s, DB Size, RDS CPU.*

"The database is often where distributed systems hit their first scaling wall, and it's the hardest component to scale horizontally. These panels let you see the pressure building before it becomes a crisis.

**DB Connections** is the most actionable metric. Each server node opens a connection pool. With 4 nodes at 25 connections each, you're at 100 out of ~170 max. If you add more nodes without watching this, you'll hit connection exhaustion and start getting errors.

**DB Operations/s** shows the mix of inserts, updates, and selects. During write-heavy workloads you expect inserts to dominate. If selects start dominating during what should be a write test, something is retrying or re-reading unexpectedly.

**RDS CPU** gives you the headroom signal. Under 40% you have room to grow. Above 80% you need to either optimize queries or upgrade the instance class."

---

### Panel 4 — Application /metrics per node: Inside the server [6:15–8:00]

*Point to: Redis Hit Rate by Operation, Active Goroutine Semaphore Slots, S3 Upload Duration.*

"This is the section that makes this tool different from generic infrastructure monitoring. These metrics come directly from the Go server's `/metrics` endpoint — instrumented by the application developer. They reveal what's happening inside the process.

**Redis Hit Rate by Operation** shows per-node, per-operation breakdown. You can see if one node is cold while others are warm, which would explain uneven latency across the cluster. You can see whether album reads or photo reads are hitting cache differently.

**Active Goroutine Semaphore Slots** is specific to our server design but generalizes to any system with bounded concurrency. We have a tiered semaphore: 64 slots for small file uploads, 8 for large files, per node. This panel shows how many slots are currently occupied.
- If the small slot gauge is pinned at 64, you're bandwidth-saturated on small files — you might want to scale up.
- If it's consistently near zero, you have unused capacity.
- A spike followed by a drop is a burst — normal. A sustained plateau at maximum is a bottleneck.

*ChaosArena context:* S15 was a large-file upload test — 200 MB files. The large semaphore slots pinned at 8 per node (32 total), meaning each slot got roughly 3 Gbps of network bandwidth. That's why 200 MB uploaded in under 6 seconds.

**S3 Upload Duration** — p50, p95, p99 of the upload goroutine. This is the end-to-end time from 'goroutine starts' to 'S3 confirms write.' In any system that writes to external storage, this is a critical metric. If S3 has a regional issue or you're bandwidth-constrained, this panel shows it before the ALB p95 panel reacts — because it's measuring the internal operation, not the client-visible response."

---

### Panel 5 — S3 Photo Storage: External storage health [8:00–8:45]

*Point to: Total Objects, Total Bucket Size, S3 Upload Duration.*

"S3 represents the external storage tier — in a real system this could be S3, GCS, Azure Blob, or any object store. The key observability questions are: is data actually landing? How fast is the bucket growing?

**Total Objects** and **Total Bucket Size** confirm end-to-end data persistence. If you see upload requests succeeding at the API layer but this counter not growing, something is failing silently in the background goroutines.

This is the kind of silent failure that's hardest to catch without instrumentation. The HTTP layer returns 202 — the client thinks it succeeded. But the background upload to S3 failed. Without this panel, you'd only discover the problem when a user tries to fetch their photo and gets a 404."

---

### Panel 6 — DevOps Chat: AI-assisted analysis [8:45–9:15]

*Point to the Claude DevOps Assistant iframe.*

"Finally — the DevOps Chat. This is an AI assistant embedded directly in the dashboard. It has access to the live Prometheus API. You can ask it natural language questions: 'why is p95 high right now?', 'which node is the slowest?', 'is the cache warm?'

It pulls the metrics, reasons about them, and gives you an explanation. This is the direction monitoring tools are going — not just showing you numbers, but helping you interpret them."

---

## PART 2 — Experiments: Does the Monitoring Actually Work? (4 min) [10:00–14:00]

> *Switch to experiment graphs in `experiments/graphs/` or terminal.*

"The live dashboard shows the tool in action. But we also wanted to validate the monitoring system itself — not just the album store. The question is: does the observability stack accurately reflect what's actually happening, and does it add enough overhead to matter? We ran seven structured experiments to answer that."

---

### Experiment 1 — Kafka Pipeline Throughput Under Load [10:00–10:50]

*Show `fig1_kafka_pipeline_throughput.png`*

"Every HTTP request produces a Kafka event. The question is: does Kafka keep up as load increases, or does it become a bottleneck that distorts the metrics?

| Users | req/s | p50 latency | p95 latency |
|-------|-------|-------------|-------------|
| 10    | 32    | 15ms        | 190ms       |
| 30    | 94    | 16ms        | 350ms       |
| 60    | 164   | 18ms        | 670ms       |
| 100   | 219   | 19ms        | 710ms       |

Zero failures across all loads. p50 moves only 4ms across a 7× increase in throughput. This confirms the Kafka producer is truly non-blocking — it does not add measurable latency to the HTTP response path.

The general lesson: if your observability pipeline adds overhead to the thing it's observing, your metrics are lying to you. This experiment proves ours doesn't."

---

### Experiment 2 — Cache Observability: Does the Panel Reflect Reality? [10:50–11:35]

*Show `fig3_cache_observability.png`*

"This experiment validates the Redis Hit Rate panel. We ran two identical workloads — one with a cold cache, one warm — and checked whether Grafana's hit rate accurately tracked the difference.

| Scenario    | req/s | p95   | Hit rate shown in Grafana |
|-------------|-------|-------|--------------------------|
| Cold cache  | 83    | 820ms | ~20% at start → climbs   |
| Warm cache  | 83    | 750ms | ~99% immediately         |

Same throughput, 8% lower p95 when warm. And Grafana showed exactly that — hit rate climbing in real time from 20% to 99% over the first 30 seconds of the cold run.

This matters because cache hit rate is one of those metrics that developers often add to a dashboard without verifying it measures what they think. This experiment confirmed our panel is measuring real behavior, not noise."

---

### Experiment 3 — Alert Detection Speed [11:35–12:15]

*Show `fig2_alert_detection.png` and `fig6_alert_evaluation.png`*

"The alert service consumes the same Kafka stream and evaluates per-instance p95 latency every 10 seconds. We wanted to know: how quickly does it detect a threshold breach?

| Load       | req/s | p95   | Alert fired? |
|------------|-------|-------|--------------|
| Low load   | 18    | 650ms | No           |
| High load  | 135   | 760ms | Yes — within 10s |

Detection within a single 10-second evaluation window. In a production system this means that if a node slows down — due to resource exhaustion, a noisy neighbor, a query regression — you get an email before the second minute of impact.

The general takeaway: real-time alerting requires a push-based pipeline, not a pull-based scraper. Prometheus scrapes every 15 seconds; Kafka delivers every event. Alert speed is bounded by the evaluation window, not the scrape interval."

---

### Experiment 4 — Monitoring Overhead [12:15–12:45]

*Show `fig5_prometheus_coverage.png`*

"The hardest objection to instrumented monitoring is: 'does it slow down my server?' We measured directly.

| Load     | req/s | p95   |
|----------|-------|-------|
| Baseline | 2.1   | 530ms |
| Medium   | 56    | 720ms |

The Kafka producer is a non-blocking channel write — sub-microsecond. The Prometheus counter increments are atomic operations — ~1μs per request. The overhead is negligible at every load level we tested.

Monitoring a system should not degrade the system. Ours doesn't."

---

### Experiment 5 — Ramp vs Spike: Does the Dashboard Tell the Difference? [12:45–13:30]

*Show `fig4_data_flow_timing.png`*

"We ran two traffic patterns that reach similar peak loads but through different shapes — a gradual ramp and an instantaneous spike — to see if the monitoring stack distinguishes them.

- **Ramp** (5 → 60 users over 3 min): 26,607 requests, p50=20ms, p95=260ms. Goroutine semaphore slots stay below 50% throughout. The Redis ops/s panel shows a smooth upward curve.
- **Spike** (0 → 80 users instantly): 10,614 requests, p50=18ms, p95=1,100ms. Goroutine semaphore slots pin at maximum instantly, then recover.

Spike p95 is 4× higher than ramp p95 even though peak users are only 33% more. The dashboard makes this visible immediately: the goroutine semaphore chart shows the spike as a vertical wall, the ramp as a gradual slope.

This is the kind of insight that lets you distinguish 'we need more capacity' (sustained plateau) from 'we need a rate limiter' (wall-shaped spike)."

---

## CLOSING — General Takeaways (1 min) [14:00–15:00]

"Let me step back from the ChaosArena-specific context and summarize the general value this stack provides to any distributed backend system.

**You get two complementary views:**
- Pull-based Prometheus for trend analysis and alerting over time
- Push-based Kafka for real-time per-request event streaming

**You get three layers of visibility:**
- Infrastructure: the cloud services — database, cache, storage, load balancer
- Application: what the server process is actually doing internally — goroutine counts, cache behavior, upload throughput
- Pipeline: the async work happening after the HTTP response — the part most monitoring tools miss

**The key design principle is that it's customizable, not plug-and-play.** You decide what to instrument in your application. The framework handles collection, storage, and visualization. This makes it applicable to any backend server — not just album stores.

The result for us: 190 out of 190 on ChaosArena, rank 12 out of 73 teams. Not because we had the fastest server — but because we could see exactly what was happening and tune accordingly."

---

## QUICK REFERENCE

| URL | Purpose |
|-----|---------|
| `http://44.243.62.10:3000` | Grafana (admin/admin) |
| `http://44.243.62.10:9090` | Prometheus |
| `http://44.243.62.10:8089` | DevOps Chat |
| `http://album-store-alb-1922764799.us-west-2.elb.amazonaws.com` | Album Store ALB |

**Start load during demo:**
```bash
cd ~/cs6650_final/album-store-monitor/loadtest
locust -f locustfile.py \
  --host http://album-store-alb-1922764799.us-west-2.elb.amazonaws.com \
  --users 80 --spawn-rate 10 --run-time 15m --headless &
```

**Stop load:**
```bash
pkill -f locust
```

**Timing guide (full ~15 min version):**
| Section | Time | Cumulative |
|---------|------|-----------|
| Opening — the problem | 1:00 | 1:00 |
| Architecture diagram | 2:00 | 3:00 |
| HTTP / ALB panels | 1:15 | 4:15 |
| Redis panels | 1:15 | 5:30 |
| RDS panels | 0:45 | 6:15 |
| Application /metrics | 1:45 | 8:00 |
| S3 panels | 0:45 | 8:45 |
| DevOps Chat | 0:30 | 9:15 |
| Transition to experiments | 0:45 | 10:00 |
| Exp 1 — Kafka throughput | 0:50 | 10:50 |
| Exp 2 — Cache observability | 0:45 | 11:35 |
| Exp 3 — Alert detection | 0:40 | 12:15 |
| Exp 4 — Monitoring overhead | 0:30 | 12:45 |
| Exp 5 — Ramp vs spike | 0:45 | 13:30 |
| Closing | 1:00 | 14:30 |
| Buffer / Q&A | 0:30 | 15:00 |

**If you need to fit in 12 min:** Drop Exp 4 (overhead) and Exp 5 (ramp/spike) — they're the least visual. Keep Exp 1, 2, 3 which have the strongest graphs.
