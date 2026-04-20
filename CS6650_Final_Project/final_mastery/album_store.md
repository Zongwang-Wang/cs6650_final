# CS 6650 — ChaosArena Contract v1: Album Store

This document is the official specification for the Album Store contract. Build and deploy a service that satisfies every endpoint and behavioral requirement below. ChaosArena will test your live deployment and score it automatically.

---

## Quick Reference

| Item                | Value                                                    |
| ------------------- | -------------------------------------------------------- |
| Contract name       | `v1-album-store`                                         |
| Max score           | 190 points                                               |
| Submission endpoint | `POST /submit` to the ChaosArena URL provided by your TA |

### _ChaosArena URL: chaosarena-alb-938452724.us-west-2.elb.amazonaws.com_

## 1. What You Must Build

A REST API accessible via a single public base URL implementing:

1. **Health check** — report that the system is alive
2. **Album store** — create, retrieve, and list albums
3. **Async photo pipeline** — accept photo uploads and process them asynchronously
4. **Photo delete** — remove a photo and its file from all storage

Your choice of language, framework, database, and cloud architecture is entirely up to you.

---

## 2. API Specification

All responses must have `Content-Type: application/json` unless otherwise noted.

### 2.1 Health Check

```
GET /health
```

**Response — 200 OK:**

```json
{ "status": "ok" }
```

The `status` field must be the exact lowercase string `"ok"`. Additional fields are allowed.

---

### 2.2 Create or Update an Album

```
PUT /albums/:album_id
Content-Type: application/json
```

**Request body:**

```json
{
  "album_id": "a1b2c3d4-...",
  "title": "My Summer Trip",
  "description": "Photos from Cancun",
  "owner": "student@northeastern.edu"
}
```

**Response — 200 or 201:**

```json
{
  "album_id": "a1b2c3d4-...",
  "title": "My Summer Trip",
  "description": "Photos from Cancun",
  "owner": "student@northeastern.edu"
}
```

This endpoint is **idempotent** — calling it twice with the same `:album_id` must not create two records.

---

### 2.3 Get an Album

```
GET /albums/:album_id
```

**Response — 200 OK:**

```json
{
  "album_id": "a1b2c3d4-...",
  "title": "My Summer Trip",
  "description": "Photos from Cancun",
  "owner": "student@northeastern.edu"
}
```

All four fields must be present and match exactly what was stored.

**Response — 404 Not Found:**

```json
{ "error": "not found" }
```

---

### 2.4 List All Albums

```
GET /albums
```

**Response — 200 OK** — bare array or wrapped object (both accepted):

```json
[
  { "album_id": "a1b2c3d4-...", "title": "My Summer Trip", ... }
]
```

```json
{ "albums": [ ... ] }
```

The list must include **every** album that has been created. Each item must contain at minimum the `album_id` field.

> **Note:** ChaosArena accumulates albums across all test scenarios including load tests. Your implementation must return all of them.

---

### 2.5 Upload a Photo (Async)

```
POST /albums/:album_id/photos
Content-Type: multipart/form-data
```

**Form field:**

| Field   | Value             |
| ------- | ----------------- |
| `photo` | binary image file |

**Response — 202 Accepted:**

```json
{
  "photo_id": "f1e2d3c4-...",
  "seq": 4,
  "status": "processing"
}
```

The 202 response must be returned **immediately**. Processing happens in the background. ChaosArena will poll the status endpoint until the photo reaches `completed`.

**`seq` — per-album photo sequence number:**

- A positive integer assigned **synchronously in the POST handler** (not by the background worker).
- Monotonically increasing and unique within each album: the first photo in an album gets `seq=1`, the second gets `seq=2`, and so on.
- Counters are **per-album** — album A and album B each have independent sequences starting at 1.
- Must also be present in `GET /albums/:album_id/photos/:photo_id` at all lifecycle stages.

---

### 2.6 Get Photo Status

```
GET /albums/:album_id/photos/:photo_id
```

**Response — 200 OK (processing):**

```json
{
  "photo_id": "f1e2d3c4-...",
  "album_id": "a1b2c3d4-...",
  "seq": 4,
  "status": "processing"
}
```

**Response — 200 OK (completed):**

```json
{
  "photo_id": "f1e2d3c4-...",
  "album_id": "a1b2c3d4-...",
  "seq": 4,
  "status": "completed",
  "url": "https://..."
}
```

The `url` field must be present when `status` is `completed`. It must be a real URL that returns 200 when fetched directly — ChaosArena will fetch it.

**Response — 404 Not Found:**

```json
{ "error": "not found" }
```

Valid status values: `"processing"` · `"completed"` · `"failed"`

---

### 2.7 Delete a Photo

```
DELETE /albums/:album_id/photos/:photo_id
```

**Response — 200 OK or 204 No Content**

The deletion must complete within **5 seconds**. After a successful DELETE:

- `GET /albums/:album_id/photos/:photo_id` must return **404**
- The file at the `url` captured before deletion must no longer return 200

---

## 3. Scoring

### 3.1 Correctness Scenarios (110 pts)

Run in order. **Critical scenarios** (marked ✅): if any critical scenario fails, all remaining scenarios are skipped.

| Scenario                         | Critical | Points  |
| -------------------------------- | -------- | ------- |
| **S1** Health Check              | ✅       | 5       |
| **S2** Album Create + Read       | ✅       | 15      |
| **S3** Async Photo Upload        | ✅       | 20      |
| **S4** Photo Delete              | ✅       | 10      |
| **S5** List Albums               | ✅       | 10      |
| **S6** Strict Health Body        |          | 5       |
| **S7** Delete (Intermediate)     |          | 10      |
| **S8** Delete (Advanced)         |          | 10      |
| **S9** Delete (Super)            |          | 10      |
| **S10** Per-Album Photo Sequence |          | 15      |
| **Subtotal**                     |          | **110** |

S5–S10 failures do not stop load tests (only S1–S5 critical failures stop everything).

A zero-point **health probe** (`GET /health`) is automatically run between every pair of scenarios. It does not affect your score, but its pass/fail status appears in your run report so you can tell whether your service was still reachable at each point in the test sequence.

---

### 3.2 Load Testing Scenarios (80 pts)

Load tests always run as long as S1–S5 pass. Scored by p95 latency.

**Scoring formula:**

```
If Student_P95 > 5 × Ref_P95:
    Score = 0

Otherwise:
    Latency Score = min(MaxPoints, MaxPoints × (Ref_P95 / Student_P95))
    Error Penalty = min(1.0, ErrorRate / 0.10)
    Score         = Latency Score × (1 − Error Penalty)
```

- `Ref_P95` — reference p95 benchmarked against a reference implementation by the TA.
- `ErrorRate` — 5xx responses + timeouts only. 4xx responses are not counted as errors.
- At 10% error rate the full score is zeroed regardless of latency.
- Beating the reference is capped at MaxPoints.

| Scenario                          | Max Points | What is measured                                                                                           |
| --------------------------------- | ---------- | ---------------------------------------------------------------------------------------------------------- |
| **S11** Concurrent Album Creates  | 15         | p95 latency of concurrent PUT /albums requests                                                             |
| **S12** Concurrent Photo Uploads  | 15         | p95 of POST→completed time under concurrent photo upload load                                              |
| **S13** Mixed Read/Write Metadata | 15         | p95 of mixed GET/PUT metadata operations running concurrently                                              |
| **S14** Mixed Metadata + Uploads  | 15 (10+5)  | metadata ops and photo uploads running simultaneously; two independent p95 sub-scores                      |
| **S15** Large Payload Upload      | 20 (10+10) | large concurrent uploads; two independent p95 sub-scores: POST→202 accept latency + POST→completed latency |
| **Subtotal**                      | **80**     |                                                                                                            |

**Grand total: 190 pts**

_TIPS: Oops, we "forgot" to publish the exact details of the load-testing scenarios! But we will give you one small hint: the engine runs a health check in-between each test. Make of that what you will! : )_

---

## 4. How to Submit

```bash
curl -X POST https://<chaosarena-url>/submit \
  -H "Content-Type: application/json" \
  -d '{
    "email":    "your@northeastern.edu",
    "nickname": "your-nickname",
    "base_url": "http://your-service-url",
    "contract": "v1-album-store"
  }'
```

You will receive a `run_id`. Poll for results:

```bash
curl https://<chaosarena-url>/runs/<run_id>
```

Status transitions: `queued → running → completed`. The full report including p95/p99 metrics and per-scenario event logs is available once status is `completed`.

**Your highest score is kept.** Submit as many times as you want.

---

## 5. Leaderboard

```bash
curl https://<chaosarena-url>/leaderboard
```

Returns all students who have submitted at least once, sorted by highest score descending.

```json
[
  {
    "rank": 1,
    "nickname": "Tiger",
    "score": 189,
    "correctness_score": 110,
    "load_score": 79
  },
  {
    "rank": 2,
    "nickname": "Panda",
    "score": 175,
    "correctness_score": 105,
    "load_score": 70
  }
]
```

`correctness_score` is the sum of points from S1–S10. `load_score` is the sum from S11–S15.

Your nickname is set from the `nickname` field in your most recent submission.

---

## 6. Debugging Failed Runs

### Reading the event log

Every correctness scenario returns a timestamped event log. Read it top to bottom:

```json
{
  "name": "S3_PHOTO_UPLOAD_ASYNC",
  "status": "FAILED",
  "points_awarded": 0,
  "events": [
    { "t_ms": 0, "type": "REQUEST", "detail": "POST /albums/xyz/photos" },
    { "t_ms": 45, "type": "RESPONSE", "detail": "status=202 latency=45ms" },
    {
      "t_ms": 30041,
      "type": "VIOLATION",
      "detail": "photo still 'processing' after 30s timeout"
    }
  ]
}
```

### Reading load test results

```json
{
  "name": "S11_CONCURRENT_CREATES_LOAD",
  "status": "PASSED",
  "points_awarded": 12,
  "metrics": {
    "duration_ms": 8241,
    "p95_ms": 187,
    "p99_ms": 312,
    "error_rate": 0.002
  }
}
```

For S14 and S15, the `extra` field breaks down sub-scores:

```json
"extra": {
  "accept_p95_ms": 4200,
  "accept_score": 10,
  "complete_p95_ms": 9100,
  "complete_score": 7,
  "complete_error_rate_pct": 0
}
```

### Common failure patterns

**Embrace the new era, ask your AI! : ) Good luck!**
