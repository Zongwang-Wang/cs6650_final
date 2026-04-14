#!/usr/bin/env python3
"""
Minimal S3 bucket metrics exporter for Prometheus.
Queries the S3 API every SCRAPE_INTERVAL seconds and exposes:
  s3_bucket_objects_total   — number of objects in the bucket
  s3_bucket_size_bytes      — total size of all objects in bytes

Why not CloudWatch? S3 storage metrics (BucketSizeBytes, NumberOfObjects)
only update once per 24 hours in CloudWatch. This exporter gives real-time data.
"""

import os
import time
import boto3
from http.server import HTTPServer, BaseHTTPRequestHandler

BUCKET   = os.environ.get("S3_BUCKET", "")
REGION   = os.environ.get("AWS_REGION", "us-west-2")
PORT     = int(os.environ.get("PORT", "9399"))
INTERVAL = int(os.environ.get("SCRAPE_INTERVAL", "60"))

s3 = boto3.client("s3", region_name=REGION)

# Cache so every Prometheus scrape doesn't hammer S3 API
_cache = {"objects": 0, "bytes": 0, "updated": 0}

def refresh():
    total_objects = 0
    total_bytes   = 0
    paginator = s3.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=BUCKET):
        for obj in page.get("Contents", []):
            total_objects += 1
            total_bytes   += obj.get("Size", 0)
    _cache["objects"] = total_objects
    _cache["bytes"]   = total_bytes
    _cache["updated"] = time.time()
    print(f"[s3-exporter] {BUCKET}: {total_objects} objects, {total_bytes:,} bytes")

def metrics_text():
    return (
        f"# HELP s3_bucket_objects_total Number of objects in the S3 bucket.\n"
        f"# TYPE s3_bucket_objects_total gauge\n"
        f's3_bucket_objects_total{{bucket="{BUCKET}"}} {_cache["objects"]}\n'
        f"# HELP s3_bucket_size_bytes Total size of all objects in the S3 bucket.\n"
        f"# TYPE s3_bucket_size_bytes gauge\n"
        f's3_bucket_size_bytes{{bucket="{BUCKET}"}} {_cache["bytes"]}\n'
    )

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass  # suppress access logs
    def do_GET(self):
        if self.path == "/metrics":
            body = metrics_text().encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; version=0.0.4")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        elif self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
        else:
            self.send_response(404)
            self.end_headers()

def background_refresh():
    import threading
    def loop():
        while True:
            try:
                refresh()
            except Exception as e:
                print(f"[s3-exporter] error: {e}")
            time.sleep(INTERVAL)
    t = threading.Thread(target=loop, daemon=True)
    t.start()

if __name__ == "__main__":
    if not BUCKET:
        raise SystemExit("S3_BUCKET env var required")
    print(f"[s3-exporter] starting: bucket={BUCKET} port={PORT} interval={INTERVAL}s")
    refresh()  # initial fetch before serving
    background_refresh()
    HTTPServer(("", PORT), Handler).serve_forever()
