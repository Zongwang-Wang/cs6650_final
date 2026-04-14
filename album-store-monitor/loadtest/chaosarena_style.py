"""
chaosarena_style.py — Reverse-engineered ChaosArena load test scenarios.

Mirrors what ChaosArena runs for S11–S15, adapted for local machines:
  - Lower concurrency (no need for 4× c5n.large to get meaningful results)
  - Same endpoint patterns and measurement approach
  - Works against the ALB directly
  - Measures p95 latency just like ChaosArena does

Usage:
  locust -f chaosarena_style.py --host http://<ALB> \\
         --users 20 --spawn-rate 5 --run-time 3m --headless

Or select a specific scenario:
  locust -f chaosarena_style.py --host http://<ALB> \\
         --tags s12 --users 30 --spawn-rate 10 --run-time 2m --headless
"""

import uuid
import random
import time
from io import BytesIO
from locust import HttpUser, task, between, tag, events


# ── S11: Concurrent Album Creates ─────────────────────────────────────────────
# ChaosArena: many concurrent PUT /albums requests
# Measures: p95 latency of PUT /albums
class S11AlbumCreates(HttpUser):
    """S11 — Concurrent album creates. Mimics ChaosArena's PUT /albums flood."""
    weight = 1
    wait_time = between(0.05, 0.2)

    @task
    @tag("s11", "creates")
    def create_album(self):
        aid = str(uuid.uuid4())
        self.client.put(
            f"/albums/{aid}",
            json={
                "album_id": aid,
                "title": f"S11 {aid[:8]}",
                "description": "chaosarena s11 load test",
                "owner": "locust@test.com",
            },
            name="/albums/:id [PUT] S11",
        )


# ── S12: Concurrent Photo Uploads ─────────────────────────────────────────────
# ChaosArena: concurrent POST /albums/:id/photos + poll until completed
# Measures: p95 of POST→completed time
class S12PhotoUploads(HttpUser):
    """S12 — Concurrent photo uploads. Tracks POST→completed latency."""
    weight = 2
    wait_time = between(0.5, 1.5)
    _album_id = None

    def on_start(self):
        aid = str(uuid.uuid4())
        self.client.put(
            f"/albums/{aid}",
            json={"album_id": aid, "title": "S12", "description": "s12", "owner": "l@t.com"},
            name="/albums/:id [PUT] S12 setup",
        )
        self._album_id = aid

    @task
    @tag("s12", "photos")
    def upload_and_poll(self):
        """Measures the full POST→completed round trip like ChaosArena."""
        data = b"X" * (1 * 1024 * 1024)  # 1MB — representative small photo
        start = time.time()

        with self.client.post(
            f"/albums/{self._album_id}/photos",
            files={"photo": ("photo.bin", BytesIO(data), "image/jpeg")},
            name="/albums/:id/photos [POST] S12",
            catch_response=True,
        ) as resp:
            if resp.status_code != 202:
                resp.failure(f"expected 202, got {resp.status_code}")
                return
            photo_id = resp.json()["photo_id"]
            resp.success()

        # Poll until completed (like ChaosArena does, up to 30s)
        deadline = time.time() + 30
        while time.time() < deadline:
            status_resp = self.client.get(
                f"/albums/{self._album_id}/photos/{photo_id}",
                name="/albums/:id/photos/:id [GET poll] S12",
            )
            if status_resp.status_code == 200:
                status = status_resp.json().get("status", "")
                if status in ("completed", "failed"):
                    elapsed_ms = (time.time() - start) * 1000
                    # Log as a custom timing event
                    self.environment.events.request.fire(
                        request_type="CUSTOM",
                        name="POST→completed S12 (ms)",
                        response_time=elapsed_ms,
                        response_length=0,
                        context={},
                        exception=None if status == "completed" else Exception("failed"),
                    )
                    return


# ── S13: Mixed Read/Write Metadata ─────────────────────────────────────────────
# ChaosArena: interleaved GET + PUT album operations
# Measures: p95 latency of mixed metadata ops
class S13MixedMetadata(HttpUser):
    """S13 — Mixed GET/PUT metadata. Tests cache hit path and write-through."""
    weight = 3
    wait_time = between(0.02, 0.15)
    _album_ids: list[str] = []

    def on_start(self):
        for _ in range(3):
            aid = str(uuid.uuid4())
            self.client.put(
                f"/albums/{aid}",
                json={"album_id": aid, "title": f"S13 {aid[:8]}", "description": "s13", "owner": "l@t.com"},
                name="/albums/:id [PUT] S13 setup",
            )
            self._album_ids.append(aid)

    @task(5)
    @tag("s13", "metadata", "read")
    def get_album(self):
        if self._album_ids:
            aid = random.choice(self._album_ids)
            self.client.get(f"/albums/{aid}", name="/albums/:id [GET] S13")

    @task(2)
    @tag("s13", "metadata", "write")
    def update_album(self):
        if self._album_ids:
            aid = random.choice(self._album_ids)
            self.client.put(
                f"/albums/{aid}",
                json={"album_id": aid, "title": f"Updated {aid[:8]}", "description": "updated", "owner": "l@t.com"},
                name="/albums/:id [PUT] S13",
            )

    @task(1)
    @tag("s13", "metadata")
    def list_albums(self):
        self.client.get("/albums", name="/albums [LIST] S13")


# ── S14: Mixed Metadata + Photo Uploads ────────────────────────────────────────
# ChaosArena: metadata ops AND photo uploads running simultaneously
# Two sub-scores: metadata p95 + upload p95 independently measured
class S14Mixed(HttpUser):
    """S14 — Mixed metadata + uploads simultaneously."""
    weight = 2
    wait_time = between(0.1, 0.5)
    _album_id = None

    def on_start(self):
        aid = str(uuid.uuid4())
        self.client.put(
            f"/albums/{aid}",
            json={"album_id": aid, "title": "S14", "description": "s14", "owner": "l@t.com"},
            name="/albums/:id [PUT] S14 setup",
        )
        self._album_id = aid

    @task(4)
    @tag("s14", "metadata")
    def metadata_op(self):
        """Metadata ops — these must stay fast even with concurrent uploads."""
        self.client.get(f"/albums/{self._album_id}", name="/albums/:id [GET] S14 meta")

    @task(1)
    @tag("s14", "upload")
    def upload_photo(self):
        """Background uploads compete for bandwidth with metadata ops."""
        data = b"Y" * (5 * 1024 * 1024)  # 5MB
        self.client.post(
            f"/albums/{self._album_id}/photos",
            files={"photo": ("photo.bin", BytesIO(data), "image/jpeg")},
            name="/albums/:id/photos [POST] S14 upload",
        )


# ── S15: Large Payload Upload ──────────────────────────────────────────────────
# ChaosArena: large concurrent uploads — two sub-scores:
#   1. POST→202 accept latency
#   2. POST→completed time
# Uses smaller files than ChaosArena's 200MB to work on local machines.
# Scale files up if you want to stress-test S3 bandwidth.
class S15LargePayload(HttpUser):
    """S15 — Large payload uploads. Reduced file size for local testing."""
    weight = 1
    wait_time = between(2.0, 5.0)   # slower spawn — large files need bandwidth
    _album_id = None
    # ChaosArena sends up to 200MB; use 20MB for local demo (same code path)
    FILE_SIZE_MB = 20

    def on_start(self):
        aid = str(uuid.uuid4())
        self.client.put(
            f"/albums/{aid}",
            json={"album_id": aid, "title": "S15 Large", "description": "s15", "owner": "l@t.com"},
            name="/albums/:id [PUT] S15 setup",
        )
        self._album_id = aid

    @task
    @tag("s15", "large")
    def large_upload_and_poll(self):
        data = b"Z" * (self.FILE_SIZE_MB * 1024 * 1024)
        accept_start = time.time()

        with self.client.post(
            f"/albums/{self._album_id}/photos",
            files={"photo": ("large.bin", BytesIO(data), "image/jpeg")},
            name=f"/albums/:id/photos [POST {self.FILE_SIZE_MB}MB] S15",
            catch_response=True,
            timeout=120,
        ) as resp:
            accept_ms = (time.time() - accept_start) * 1000
            if resp.status_code != 202:
                resp.failure(f"expected 202, got {resp.status_code}")
                return
            photo_id = resp.json()["photo_id"]
            resp.success()

        # Measure accept latency as a separate event
        self.environment.events.request.fire(
            request_type="CUSTOM",
            name=f"POST→202 accept S15 ({self.FILE_SIZE_MB}MB)",
            response_time=accept_ms,
            response_length=0,
            context={},
            exception=None,
        )

        # Poll until completed — measures complete latency
        complete_start = time.time()
        deadline = time.time() + 60
        while time.time() < deadline:
            r = self.client.get(
                f"/albums/{self._album_id}/photos/{photo_id}",
                name="/albums/:id/photos/:id [GET poll] S15",
            )
            if r.status_code == 200 and r.json().get("status") in ("completed", "failed"):
                complete_ms = (time.time() - complete_start) * 1000 + accept_ms
                self.environment.events.request.fire(
                    request_type="CUSTOM",
                    name=f"POST→completed S15 ({self.FILE_SIZE_MB}MB)",
                    response_time=complete_ms,
                    response_length=0,
                    context={},
                    exception=None if r.json().get("status") == "completed" else Exception("failed"),
                )
                return
