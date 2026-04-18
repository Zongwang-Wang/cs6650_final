# Demo Script — Album Store Monitor
**"Building Observability for Distributed Backend Systems"**
**Total time: ~15 minutes** *(adjust pacing to hit 12 if needed — experiments are droppable)*

---

## OPENING — The Problem (1 min) [0:00–1:00]

"Today we're talking about monitoring and observability — a problem every backend server faces, and one that becomes significantly harder in a distributed system. 

The tool we built is a real-time observability stack for distributed backend servers. It is not plug-and-play. It's customizable — you decide what to measure, what thresholds matter, what to alert on. But once configured, it gives you a complete picture of your system from the infrastructure layer down to what's happening inside each server process.

ChaosArea, is a excellent exmaple we can demo our tool, because we know very little what the load is actually like. So, we decided to mount our tool to chaos area to do a demo.

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

Two parallel monitoring channels feed into Grafana.
Kafka (event-driven) connects two consumers: an Analytics service that aggregates events into 60-second windows, and an Alert service that evaluates per-instance p95 latency every 10 seconds and fires SNS email alerts on threshold breaches.
Prometheus (pull-based) scrapes eleven endpoints every 15 seconds — the album store, both microservices, and exporters for Redis, PostgreSQL, CloudWatch, and S3.
Together they provide two granularities: real-time per-event alerting and time-series trend analysis.

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

- **Request Rate** — how much traffic is hitting the load balancer. 
- **5xx Error Rate** — the most critical signal. Any non-zero value here means requests are failing. 
- **P95 Latency** — the experience of the slowest 5% of your users. 
- **Healthy Nodes** — tells you immediately if a node dropped out of the load balancer. 

---

### Panel 2 — Redis / ElastiCache: Cache behavior and seq counters [4:15–5:30]

*Point to: Cache Hit Rate, Cache Hits vs Misses, Seq Keys, Redis Memory.*

Redis handles two roles here: caching and coordination.

**Caching:** the Cache Hit Rate panel shows how many reads are served from memory vs falling through to the database. A high hit rate means low DB load and fast responses — and a sudden drop, say after a deployment that flushes the cache, immediately explains an otherwise mysterious latency spike.

**Coordination:** every photo upload across all 4 nodes calls `INCR` on a shared Redis key for a globally atomic sequence number. The Seq Keys panel confirms those counters are active and working.

*ChaosArena S13:* after the cache warmed up in the first 30 seconds, hit rate climbed to 99% and p95 dropped to 6ms — this panel made that relationship visible in real time.

---

### Panel 3 — RDS PostgreSQL: Database pressure [5:30–6:15]

*Point to: DB Connections, DB Operations/s, DB Size, RDS CPU.*

"The database is often where distributed systems hit their first scaling wall, and it's the hardest component to scale horizontally. These panels let you see the pressure building before it becomes a crisis.

**DB Connections** is the most actionable metric. Each server node opens a connection pool. 

**DB Operations/s** shows the mix of inserts, updates, and selects. During write-heavy workloads you expect inserts to dominate. 

**RDS CPU** gives you the headroom signal. Under 40% you have room to grow. Above 80% you need to either optimize queries or upgrade the instance class."

---

### Panel 4 — Application /metrics per node: Inside the server [6:15–8:00]

---

*Point to: Redis Hit Rate by Operation, Active Goroutine Semaphore Slots, S3 Upload Duration.*

**Redis Hit Rate by Operation** gives a per-node, per-operation breakdown. You can spot a cold node while others are warm, or see whether album vs photo reads are hitting cache differently.

**Active Goroutine Semaphore Slots** reflects our tiered concurrency design: 64 slots for small uploads, 8 for large, per node. The panel shows current occupancy.

this can really help us track the upload speed to s3, which is helpful for s15.


---

### Panel 5 — S3 Photo Storage: External storage health [8:00–8:45]

*Point to: Total Objects, Total Bucket Size, S3 Upload Duration.*

The key observability questions are: is data actually landing? How fast is the bucket growing?

**Total Objects** and **Total Bucket Size** confirm end-to-end data persistence. 

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

Detection within a single 10-second evaluation window. In a production system this means that if a node slows down you get an email before the second minute of impact.

The general takeaway: real-time alerting requires a push-based pipeline. Prometheus scrapes every 15 seconds; Kafka delivers every event. Alert speed is bounded by the evaluation window, not the scrape interval."

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

We ran two traffic patterns to the same peak load to see if the monitoring stack distinguishes them.

Ramp (5 → 60 users over 3 min): 26,607 requests
Spike (0 → 80 users instantly): 10,614 requests

Spike p95 is 4× higher despite only 33% more peak users. The goroutine semaphore panel makes the difference immediate: the spike looks like a vertical wall, the ramp like a gradual slope.

---

## CLOSING — General Takeaways (1 min) [14:00–15:00]

"Let me step back from the ChaosArena-specific context and summarize the general value this stack provides to any distributed backend system.

**The key design principle is that it's customizable, not plug-and-play.** You decide what to include in your application. The framework handles collection, storage, and visualization. This makes it applicable to any backend server.
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
