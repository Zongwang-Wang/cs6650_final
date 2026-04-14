"""
Album Store load test — targets the ChaosArena album store ALB.

Exercises all key metrics visible in Grafana:
  - Redis hit/miss rate  (repeated GET /albums/:id after first creation)
  - Seq INCR rate        (POST /albums/:id/photos)
  - Upload pipeline      (tracks processing → completed transition)
  - Mixed metadata load  (interleaved GET + PUT albums)

Usage:
  locust -f locustfile.py --host http://<ALB-URL> \
         --users 50 --spawn-rate 5 --run-time 3m --headless

Or with web UI:
  locust -f locustfile.py --host http://<ALB-URL>
  open http://localhost:8089
"""

import uuid
import random
import time
from locust import HttpUser, task, between, events
from locust.runners import MasterRunner


# ── Shared state (albums created during the run) ──────────────────────────────
_albums: list[str] = []          # album_ids with at least one photo uploaded
_processing: dict[str, str] = {} # photo_id → album_id, waiting for completed


# ── User profiles ──────────────────────────────────────────────────────────────

class AlbumReadUser(HttpUser):
    """Heavy reader — generates Redis cache hits after warm-up reads."""
    weight = 3
    wait_time = between(0.1, 0.5)

    def on_start(self):
        # Create one album to read repeatedly (exercises cache hit path)
        aid = str(uuid.uuid4())
        self.client.put(
            f"/albums/{aid}",
            json={"album_id": aid, "title": "Reader Test", "description": "load", "owner": "locust@test.com"},
            name="/albums/:id [PUT warm-up]",
        )
        self.album_id = aid
        _albums.append(aid)

    @task(5)
    def get_album_cache_hit(self):
        """GET same album repeatedly — first miss, then Redis hits."""
        self.client.get(f"/albums/{self.album_id}", name="/albums/:id [GET]")

    @task(2)
    def get_album_list(self):
        """GET /albums — exercises keyset pagination."""
        self.client.get("/albums", name="/albums [LIST]")

    @task(1)
    def get_random_album(self):
        """GET a random album from the shared pool (cache miss likely)."""
        if _albums:
            aid = random.choice(_albums)
            self.client.get(f"/albums/{aid}", name="/albums/:id [GET random]")

    @task(1)
    def get_nonexistent_album(self):
        """GET a brand-new UUID — guaranteed cache MISS → DB lookup → 404.
        This keeps the Redis miss counter visible even when the cache is warm.
        The 404 is expected and not counted as an error."""
        fake_id = str(uuid.uuid4())
        with self.client.get(
            f"/albums/{fake_id}",
            name="/albums/:id [GET cache-miss]",
            catch_response=True,
        ) as r:
            # 404 is the correct response for a nonexistent album
            if r.status_code == 404:
                r.success()
            else:
                r.failure(f"Expected 404, got {r.status_code}")


class PhotoUploadUser(HttpUser):
    """Uploader — exercises seq INCR, S3 pipeline, and photo status polling."""
    weight = 2
    wait_time = between(0.5, 2.0)

    def on_start(self):
        aid = str(uuid.uuid4())
        self.client.put(
            f"/albums/{aid}",
            json={"album_id": aid, "title": "Uploader", "description": "load", "owner": "locust@test.com"},
            name="/albums/:id [PUT warm-up]",
        )
        self.album_id = aid
        _albums.append(aid)

    @task(3)
    def upload_small_photo(self):
        """Upload a 100KB file — hits the small semaphore (64 slots per node)."""
        data = b"X" * 100 * 1024
        with self.client.post(
            f"/albums/{self.album_id}/photos",
            files={"photo": ("photo.bin", data, "image/jpeg")},
            name="/albums/:id/photos [POST small]",
            catch_response=True,
        ) as resp:
            if resp.status_code == 202:
                body = resp.json()
                _processing[body["photo_id"]] = self.album_id
                resp.success()
            else:
                resp.failure(f"Expected 202, got {resp.status_code}")

    @task(1)
    def upload_large_photo(self):
        """Upload a 15MB file — hits the large semaphore (8 slots per node)."""
        data = b"X" * 15 * 1024 * 1024
        with self.client.post(
            f"/albums/{self.album_id}/photos",
            files={"photo": ("big.bin", data, "image/jpeg")},
            name="/albums/:id/photos [POST large 15MB]",
            catch_response=True,
            timeout=60,
        ) as resp:
            if resp.status_code == 202:
                body = resp.json()
                _processing[body["photo_id"]] = self.album_id
                resp.success()
            else:
                resp.failure(f"Expected 202, got {resp.status_code}")

    @task(2)
    def poll_photo_status(self):
        """Poll a random in-flight photo — exercises photo status cache."""
        if not _processing:
            return
        photo_id = random.choice(list(_processing.keys()))
        album_id = _processing.get(photo_id, self.album_id)
        with self.client.get(
            f"/albums/{album_id}/photos/{photo_id}",
            name="/albums/:id/photos/:id [GET status]",
            catch_response=True,
        ) as resp:
            if resp.status_code == 200:
                status = resp.json().get("status", "")
                if status == "completed":
                    _processing.pop(photo_id, None)
                resp.success()
            elif resp.status_code == 404:
                _processing.pop(photo_id, None)
                resp.success()
            else:
                resp.failure(f"status={resp.status_code}")


class MixedUser(HttpUser):
    """Mixed metadata — exercises album cache write-through and read."""
    weight = 2
    wait_time = between(0.05, 0.3)

    def on_start(self):
        self.my_albums: list[str] = []

    @task(4)
    def create_and_read_album(self):
        """PUT then GET — first GET is cache miss, subsequent are hits."""
        aid = str(uuid.uuid4())
        self.client.put(
            f"/albums/{aid}",
            json={"album_id": aid, "title": f"Mixed {aid[:8]}", "description": "load", "owner": "m@test.com"},
            name="/albums/:id [PUT]",
        )
        self.my_albums.append(aid)
        _albums.append(aid)
        # Immediately GET — should be cache hit (write-through on PUT)
        self.client.get(f"/albums/{aid}", name="/albums/:id [GET after PUT]")

    @task(6)
    def read_existing_album(self):
        """GET an album from this user's pool — high cache hit rate."""
        if self.my_albums:
            aid = random.choice(self.my_albums)
            self.client.get(f"/albums/{aid}", name="/albums/:id [GET cached]")

    @task(1)
    def delete_photo(self):
        """DELETE a random completed photo — exercises cache invalidation."""
        pass  # placeholder — requires tracking completed photos
