# CS 6650 Final Mastery — Design Report
## ChaosArena v1-album-store · Go Implementation

**Final score: 190/190 (110/110 correctness + 80/80 load) — Rank ~12/73**
**Nickname: Zongwang Wang**

---

## 1. Architecture Overview

```
ChaosArena ALB (us-west-2)
        │  HTTP
        ▼
  AWS ALB (album-store-alb2, us-west-2)
   ├── c5n.large node #1  ─┐
   ├── c5n.large node #2   ├── all connect to shared ElastiCache Redis
   ├── c5n.large node #3   ├── all connect to shared RDS PostgreSQL
   └── c5n.large node #4  ─┘
                                │                   │                │
                         ElastiCache          RDS PostgreSQL      S3 bucket
                         cache.t3.micro       db.t3.medium        (us-west-2)
                         Redis 7              PostgreSQL 17       public-read
                         (shared seq INCR)
```

**Why this architecture achieves 190/190:**
- **4× c5n.large**: network-optimized EC2, 25 Gbps dedicated each = 100 Gbps total. No burst credits, no throttling. The decisive factor for S12 (15/15) and S15 (20/20).
- **ElastiCache (external Redis)**: shared across all 4 nodes. `INCR album:{id}:seq` is globally atomic — no duplicate seq values across instances.
- **RDS db.t3.medium**: 2 vCPUs, 4 GB RAM — faster than db.t3.micro for S11 concurrent creates.
- **S3 same-region**: 100+ Gbps internal bandwidth for uploads from EC2 to S3.

---

## 2. Technology Choices

### Language: Go
Go's goroutine model is ideal — spawning one goroutine per async upload is trivial overhead. The standard `net/http` server handles high concurrency. `ParseMultipartForm` streams large files to temp disk without developer involvement.

**Dependencies (minimal):**
| Package | Role |
|---|---|
| `go-chi/chi/v5` | Router — zero-allocation URL params |
| `jackc/pgx/v5` | PostgreSQL — prepared statements, `RowToStructByName` |
| `redis/go-redis/v9` | Redis — `INCR` for seq, read cache |
| `aws/aws-sdk-go-v2` | S3 — multipart manager for streaming uploads |
| `google/uuid` | UUID v4 for photo_id |

### Router: chi
Chosen over Gin because chi reuses contexts from a pool (zero allocation per request), uses a radix tree for O(1) routing, and is stdlib-compatible.

### Database: PostgreSQL (RDS db.t3.medium)
`db.t3.medium` over `db.t3.micro` matters for S11 (concurrent album creates hitting the DB). The extra vCPU and RAM reduces lock contention under high concurrency.

### Cache: ElastiCache Redis (shared across all nodes)
**Why external rather than on-EC2:** With 4 EC2 instances, if Redis is on one instance, the others must connect to it. AWS EC2 Redis requires careful security group + `bind 0.0.0.0` configuration that is error-prone. ElastiCache is a managed endpoint that all nodes reach identically — no bind configuration needed.

**Why Redis for seq (not just DB):** Redis `INCR` is single-threaded and returns a unique, monotonically increasing value to every concurrent caller. Sub-millisecond. A DB `UPDATE ... RETURNING` is also correct but adds ~3ms per photo upload vs ~0.1ms for Redis INCR.

### S3 for photo storage
Real fetchable URL, same-region fast uploads, IAM instance role for credentials. Public-read bucket policy makes URLs work without expiry.

---

## 3. Key Design Decisions

### 3.1 Health endpoint — pure memory, zero external calls

```go
var healthBody = []byte(`{"status":"ok"}`)
func Health(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(200)
    w.Write(healthBody)
}
```

The spec runs a health probe between every pair of scenarios. A pure-memory handler returns in microseconds regardless of DB/Redis/S3 state. S6 checks the body bytes literally — hardcoded bytes guarantee exact match.

### 3.2 Photo upload: ParseMultipartForm → 202 immediately → goroutine

The winning upload flow (matches friend's architecture diagram):

```
1. ParseMultipartForm(50MB)  — all bytes buffered (memory or disk temp file)
2. Assign seq via Redis INCR  — synchronous, in handler, per spec
3. INSERT photo row (DB)      — status=processing
4. Send 202 immediately       — client gets response fast
5. Transfer file to goroutine — goroutine acquires tiered semaphore, uploads to S3
6. Goroutine updates DB+Redis — status=completed, url=...
```

Key insight: **202 is sent after buffering, before S3 upload.** This is what makes accept_p95 fast (just receive time, no upload blocking). The goroutine uploads asynchronously.

**Why not a worker pool?** A fixed-size worker pool adds queuing overhead. Direct goroutines with a semaphore are simpler and equally controlled. Go can handle thousands of goroutines — spawning one per request is not expensive.

### 3.3 Tiered semaphore — the load test scoring key

```go
var (
    smallSem = make(chan struct{}, 64) // files < 10 MB: high concurrency
    largeSem = make(chan struct{}, 8)  // files ≥ 10 MB: preserve bandwidth
)
const largeSizeThreshold = 10 * 1024 * 1024
```

With 4 nodes:
- Small files (S12): 4 × 64 = 256 concurrent uploads. Many small files complete in parallel, p95 drops to ~1,368ms → **15/15**.
- Large files (S15): 4 × 8 = 32 concurrent uploads. Each gets 100 Gbps / 32 = ~3 Gbps → 200 MB uploads in ~0.5s. complete_p95 ≈ 6,188ms → **10/10**.

**Why 10 MB threshold (not 50 MB):** Small concurrent files in S12 need high parallelism. Large files in S15 need exclusive bandwidth. 10 MB separates "many small" (S12) from "few large" (S15) test patterns cleanly.

### 3.4 Per-album seq counter via Redis INCR

```go
func (c *Cache) NextSeq(ctx context.Context, albumID string) (int64, error) {
    return c.client.Incr(ctx, "album:"+albumID+":seq").Result()
}
```

Redis `INCR` is guaranteed atomic across all 4 server nodes because all connect to the same ElastiCache endpoint. First call returns 1, second returns 2, etc. — no duplicates ever possible under any concurrency.

**Startup warm-up:** On each server start, query `MAX(seq)` per album from DB and call `InitSeq` to ensure Redis counters don't restart at 0 after a service restart.

### 3.5 Soft-delete for idempotent DELETE

```sql
UPDATE photos SET status = 'deleted', url = NULL
WHERE photo_id = $1 AND album_id = $2
```

Three benefits:
1. **Double-DELETE idempotent (S7/S8):** Second call finds `status='deleted'`, returns 204, not 5xx.
2. **Delete-during-upload race (S9):** Worker calls `UpdateStatus` with `WHERE status != 'deleted'`. If deleted, 0 rows affected → worker cleans up S3 and does NOT update Redis cache to "completed". This race fix was critical — S9 failed once before the fix.
3. **GET returns 404 after DELETE:** Handler filters on `status != 'deleted'`.

**The race fix detail:** `UpdateStatus` returns `(bool, error)` — the bool is `RowsAffected() > 0`. Worker only writes to Redis cache if the DB row was actually updated. If DELETE already ran and evicted the cache, the worker won't re-add "completed" to Redis.

### 3.6 DELETE ordering (correctness guarantee)

```
1. MarkDeleted in DB        → GET immediately returns 404
2. DeletePhotoStatus Redis  → cache misses fall through to DB → 404
3. Delete from S3           → URL no longer returns HTTP 200
```

Step 1 before step 3: the URL might briefly be fetchable while S3 delete is in-flight, but GET already returns 404. ChaosArena checks these sequentially, so ordering is safe.

### 3.7 GET /albums — streaming pagination with UUID index

With 260,000+ albums accumulated across load test runs, naive buffering failed (30s timeout). Two fixes:

```sql
-- CORRECT: UUID-type comparison uses the primary key index directly
SELECT album_id, title, description, owner
FROM albums WHERE album_id > $1::uuid
ORDER BY album_id LIMIT 500

-- WRONG: text cast bypasses the UUID index → sequential scan → 30+ seconds
WHERE album_id::text > $1
```

**Streaming instead of buffering:** `StreamList` writes directly to the `http.ResponseWriter` page by page, never holding 260K albums in memory. Uses `json.NewEncoder(w)` to stream each album as it arrives from DB.

### 3.8 Idempotent album upsert

```sql
INSERT INTO albums (album_id, title, description, owner)
VALUES ($1, $2, $3, $4)
ON CONFLICT (album_id) DO UPDATE
    SET title = EXCLUDED.title, description = EXCLUDED.description, owner = EXCLUDED.owner
```

Single atomic statement. Concurrent PUTs for the same album_id — only one insert wins, others are no-ops. No application-level locking.

---

## 4. Database Schema

```sql
CREATE TABLE IF NOT EXISTS albums (
    album_id    UUID PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL,
    owner       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS photos (
    photo_id   UUID PRIMARY KEY,
    album_id   UUID NOT NULL,            -- no FK: avoids lock contention
    seq        BIGINT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'processing',
    url        TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (album_id, seq)              -- safety net (Redis INCR prevents duplicates)
);

CREATE INDEX IF NOT EXISTS idx_photos_album_id ON photos(album_id);
```

**No FK from photos.album_id to albums:** FK constraints take share locks on the parent row during child inserts. Under S12 (many concurrent photo uploads for the same album), this creates contention. The application validates album existence before inserting.

---

## 5. Performance: Final Load Test Numbers

| Scenario | Score | p95 |
|---|---|---|
| S11 Concurrent Creates | **15/15** | 14ms |
| S12 Concurrent Photo Uploads | **15/15** | 1,368ms |
| S13 Mixed Read/Write | **15/15** | 6ms |
| S14 Mixed Metadata + Uploads | **15/15** | 15ms |
| S15 Large Payload | **20/20** | accept=4,098ms · complete=6,188ms |

---

## 6. Infrastructure

| Component | Service | Spec | Rationale |
|---|---|---|---|
| HTTP server (×4) | EC2 c5n.large | 2 vCPU, 5.5 GB RAM, **25 Gbps dedicated** | Network-optimized; no burst credits |
| Database | RDS PostgreSQL 17 | db.t3.medium, 20 GB gp2 | Handles S11 concurrent creates |
| Cache + seq | ElastiCache Redis 7 | cache.t3.micro | Shared across all 4 nodes; managed endpoint |
| Photo files | S3 | us-west-2, public-read | Durable, fetchable URL, same-region fast upload |
| Load balancer | ALB | Internet-facing, port 80→8080 | Distributes load across 4 nodes |

**Estimated cost:** ~$1.60/hr for all 4 nodes + ALB + ElastiCache + RDS. For a multi-hour submission session this is well within a $40 lab budget.

---

## 7. What Didn't Work (Lessons Learned)

### t3.large burst credit exhaustion
The t3.large earns CPU/network credits at a baseline rate. After 3–4 consecutive ChaosArena load test runs, credits drop to ~13 out of 288 max, throttling network to ~256 Mbps baseline. S15 scored 0 on a throttled run. **Fix: c5n.large — dedicated network, no credit system.**

### HTTP/1.1 streaming limitation
We attempted to send 202 before reading the full request body (stream client→server→S3 in parallel). This failed because HTTP/1.1 clients close the TCP send side after receiving a complete 202 response, which closes `r.Body` on the server. The Go HTTP runtime then returns "invalid Read on closed Body." **Fix: ParseMultipartForm buffers first, 202 sent after — reliable, HTTP/1.1-safe.**

### io.ReadAll + goroutine
For large files (S15), reading into `[]byte` caused GC pressure (10 concurrent × 200MB = 2GB live heap, frequent GC stop-the-world pauses). P95 latency worsened. **Fix: ParseMultipartForm with 50MB limit — files > 50MB overflow to disk, no heap pressure.**

### Single instance bottleneck
A single m5.large (10 Gbps) or t3.large (5 Gbps burst) cannot serve enough S3 upload bandwidth for S12 (15/15) and S15 (20/20) simultaneously. The reference implementation uses multiple nodes. **Fix: 4 instances provides 100 Gbps total.**

### In-process Redis for multi-node
When running Redis on one EC2 and pointing other nodes at its private IP, the `bind 127.0.0.1` default Redis config blocks cross-instance connections, and fixing it proved unreliable via SSM. **Fix: ElastiCache provides a managed external endpoint that all nodes reach without configuration.**

### 260K accumulated albums
S11 (concurrent album creates) runs in every ChaosArena test. After multiple submissions, the albums table grew to 260,000+ rows. `GET /albums` with the original `WHERE album_id::text > $1` query triggered a sequential scan, timing out in 30+ seconds. **Fix: `WHERE album_id > $1::uuid` uses the UUID primary key index directly — 260K albums in 1.27 seconds.**

---

## 8. Architecture Evolution

| Run | Setup | Score |
|---|---|---|
| 1 | 1× t3.large, worker pool | 179/190 |
| 2-4 | Various optimizations on t3.large (credits depleting) | 166–177 |
| 5 | 1× m5.large (10 Gbps), restored worker pool | 181/190 |
| 6 | 4× m5.large + ALB (Redis bind issues) | Failed S3 |
| 7 | 4× c5n.large + ElastiCache + db.t3.medium | **188/190** |
| 8 | Same + run variance | **190/190** |
