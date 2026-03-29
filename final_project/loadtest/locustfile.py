"""
Locust load-test for the cart-api service.

Test profiles (pick one when launching Locust):
  locust -f locustfile.py SteadyUser   # constant-rate baseline
  locust -f locustfile.py RampUser     # ramp via --spawn-rate CLI flag
  locust -f locustfile.py SpikeUser    # alternating low/high bursts
  locust -f locustfile.py StepUser     # step-function load via LoadTestShape
"""

import random
import time

from locust import HttpUser, task, constant, between


# ── Profile 1: Steady-state load ────────────────────────────
class SteadyUser(HttpUser):
    """Constant 200 ms wait between requests — good for baseline measurements."""

    wait_time = constant(0.2)

    @task
    def add_to_cart(self):
        payload = {
            "product_id": random.randint(1, 1000),
            "quantity": random.randint(1, 5),
            "customer_id": random.randint(1, 10000),
        }
        self.client.post("/cart/items", json=payload)


# ── Profile 2: Ramp load (control via CLI) ──────────────────
class RampUser(HttpUser):
    """Same cadence as SteadyUser; use --spawn-rate to ramp users up gradually."""

    wait_time = constant(0.2)

    @task
    def add_to_cart(self):
        payload = {
            "product_id": random.randint(1, 1000),
            "quantity": random.randint(1, 5),
            "customer_id": random.randint(1, 10000),
        }
        self.client.post("/cart/items", json=payload)


# ── Profile 3: Spike / burst load ───────────────────────────
class SpikeUser(HttpUser):
    """Alternates between calm (1-2 s) and burst (10-50 ms) phases
    every ~30 seconds to simulate traffic spikes."""

    _phase_duration = 30  # seconds per phase

    def wait_time(self):
        phase = int(time.time() / self._phase_duration) % 2
        if phase == 0:
            return random.uniform(1.0, 2.0)   # calm
        return random.uniform(0.01, 0.05)      # burst

    @task
    def add_to_cart(self):
        payload = {
            "product_id": random.randint(1, 1000),
            "quantity": random.randint(1, 5),
            "customer_id": random.randint(1, 10000),
        }
        self.client.post("/cart/items", json=payload)


# ── Profile 4: Step-function load ────────────────────────────
class StepUser(HttpUser):
    """Steady per-user wait; pair with Locust's built-in LoadTestShape or
    the --step-load flag to increase users in discrete steps."""

    wait_time = constant(0.2)

    @task
    def add_to_cart(self):
        payload = {
            "product_id": random.randint(1, 1000),
            "quantity": random.randint(1, 5),
            "customer_id": random.randint(1, 10000),
        }
        self.client.post("/cart/items", json=payload)
