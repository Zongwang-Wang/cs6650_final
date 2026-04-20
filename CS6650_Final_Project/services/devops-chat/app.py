#!/usr/bin/env python3
"""
DevOps Chat — Album Store AI DevOps Assistant embedded in Grafana.
Gathers live Prometheus metrics and generates Claude Code prompts.
"""
import json, os, urllib.request, urllib.parse, re
from http.server import HTTPServer, BaseHTTPRequestHandler
from datetime import datetime, timezone

PROMETHEUS = os.environ.get("PROMETHEUS_URL", "http://prometheus:9090")

def prom(q):
    try:
        url = f"{PROMETHEUS}/api/v1/query?query={urllib.parse.quote(q)}"
        with urllib.request.urlopen(url, timeout=3) as r:
            d = json.load(r)
            rs = d["data"]["result"]
            if rs:
                return float(rs[0]["value"][1])
    except:
        pass
    return None

def fmt(v, unit="", dec=1):
    return f"{v:.{dec}f}{unit}" if v is not None else "N/A"

def gather():
    nodes = int(prom("count(up{job='album_store'}==1)") or 4)
    p99_raw = prom("aws_applicationelb_target_response_time_p99 * 1000")
    err_raw = prom("100 * rate(aws_applicationelb_httpcode_target_5_xx_count_sum[5m]) / (rate(aws_applicationelb_request_count_sum[5m]) + 0.001)")
    cache_raw = prom("100 * sum(rate(album_store_cache_hits_total[5m])) / (sum(rate(album_store_cache_hits_total[5m])) + sum(rate(album_store_cache_misses_total[5m])) + 0.001)")
    return {
        "time":       datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC"),
        "nodes":      str(nodes),
        "req_s":      fmt(prom("rate(aws_applicationelb_request_count_sum[5m])"), "/s"),
        "p95_ms":     fmt(prom("aws_applicationelb_target_response_time_p95 * 1000"), "ms", 0),
        "p99_ms":     fmt(p99_raw, "ms", 0),
        "err_pct":    fmt(err_raw, "%"),
        "cache_hit":  fmt(cache_raw, "%"),
        "seq_s":      fmt(prom("sum(rate(album_store_seq_incr_total[5m]))"), "/s"),
        "redis_mb":   fmt((prom("redis_memory_used_bytes") or 0)/1e6, "MB", 0),
        "db_conn":    fmt(prom("pg_stat_database_numbackends{datname='albumstore'}"), "", 0),
        "s3_objs":    fmt(prom("s3_bucket_objects_total"), "", 0),
        "p99_class":  "alert" if p99_raw and p99_raw > 5000 else ("warn" if p99_raw and p99_raw > 2000 else ""),
        "err_class":  "alert" if err_raw and err_raw > 5 else ("warn" if err_raw and err_raw > 1 else ""),
        "cache_class":"alert" if cache_raw and cache_raw < 50 else ("warn" if cache_raw and cache_raw < 80 else ""),
        "metrics_json": json.dumps({
            "nodes": nodes, "req_s": fmt(prom("rate(aws_applicationelb_request_count_sum[5m])"), "/s"),
            "p95_ms": fmt(prom("aws_applicationelb_target_response_time_p95 * 1000"), "ms", 0),
            "p99_ms": fmt(p99_raw, "ms", 0), "err_pct": fmt(err_raw, "%"),
            "cache_hit": fmt(cache_raw, "%"),
            "seq_s": fmt(prom("sum(rate(album_store_seq_incr_total[5m]))"), "/s"),
            "redis_mb": fmt((prom("redis_memory_used_bytes") or 0)/1e6, "MB", 0),
            "db_conn": fmt(prom("pg_stat_database_numbackends{datname='albumstore'}"), "", 0),
            "s3_objs": fmt(prom("s3_bucket_objects_total"), "", 0),
        }),
    }

PAGE = r"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="refresh" content="30">
<title>Album Store DevOps Chat</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: 'Segoe UI', sans-serif; background: #1a1a2e; color: #e0e0e0; padding: 10px; font-size: 12px; }
  h2 { color: #7eb8f7; margin-bottom: 6px; font-size: 14px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 3px; margin-bottom: 8px; }
  .m { background: #16213e; border-radius: 3px; padding: 4px 8px; }
  .ml { color: #666; font-size: 10px; }
  .mv { color: #7eb8f7; font-weight: bold; }
  .mv.warn { color: #f7c948; }
  .mv.alert { color: #f74848; }
  textarea { width: 100%; height: 60px; background: #0d0d1a; color: #e0e0e0; border: 1px solid #334; border-radius: 3px; padding: 6px; font-size: 11px; resize: vertical; }
  .btn { background: #7eb8f7; color: #0d0d1a; border: none; border-radius: 3px; padding: 6px; cursor: pointer; font-weight: bold; width: 100%; margin-top: 4px; font-size: 12px; }
  .btn:hover { background: #5a9fe0; }
  .cpybtn { background: #2a4a2a; color: #7ef797; font-size: 10px; padding: 3px 8px; margin-top: 3px; }
  .cpybtn:hover { background: #3a6a3a; }
  pre { background: #0d0d1a; border: 1px solid #334; border-radius: 3px; padding: 6px; margin-top: 6px; font-size: 10px; color: #999; white-space: pre-wrap; max-height: 180px; overflow-y: auto; display: none; }
  .ts { color: #444; font-size: 9px; text-align: right; }
  .sec { color: #555; font-size: 9px; text-transform: uppercase; letter-spacing: 1px; margin: 6px 0 3px; }
</style>
</head>
<body>
<h2>DevOps Assistant</h2>
<div class="ts">~~TIME~~ &bull; auto-refresh 30s</div>
<div class="sec">Live Metrics</div>
<div class="grid">
  <div class="m"><div class="ml">Nodes</div><div class="mv">~~NODES~~/4</div></div>
  <div class="m"><div class="ml">Req/s</div><div class="mv">~~REQS~~</div></div>
  <div class="m"><div class="ml">p95 lat</div><div class="mv ~~P95C~~">~~P95~~</div></div>
  <div class="m"><div class="ml">p99 lat</div><div class="mv ~~P99C~~">~~P99~~</div></div>
  <div class="m"><div class="ml">5xx err%</div><div class="mv ~~ERRC~~">~~ERR~~</div></div>
  <div class="m"><div class="ml">Cache hit%</div><div class="mv ~~CACHEC~~">~~CACHE~~</div></div>
  <div class="m"><div class="ml">Uploads/s</div><div class="mv">~~SEQ~~</div></div>
  <div class="m"><div class="ml">DB conns</div><div class="mv">~~DB~~</div></div>
</div>
<div class="sec">Ask Claude Code</div>
<textarea id="q" placeholder="e.g. Why is latency spiking? Best way to reduce costs? Should I add a node?">Analyze metrics and give top 3 optimization suggestions.</textarea>
<button class="btn" onclick="gen()">Generate Claude Code Prompt</button>
<pre id="pb"></pre>
<button class="btn cpybtn" id="cb" style="display:none" onclick="cp()">Copy to Clipboard</button>
<script>
const M = ~~MDATA~~;
function gen(){
  const q=document.getElementById('q').value.trim()||'Analyze and optimize.';
  const p=`Album Store Infrastructure Query — ${new Date().toUTCString()}

QUESTION: ${q}

LIVE METRICS:
  Nodes: ${M.nodes}/4 c5n.large (25 Gbps each)
  Req/s: ${M.req_s} | p95: ${M.p95_ms} | p99: ${M.p99_ms}
  5xx errors: ${M.err_pct} | Cache hit: ${M.cache_hit}
  Photo uploads/s: ${M.seq_s} | Redis: ${M.redis_mb}
  DB connections: ${M.db_conn} | S3 objects: ${M.s3_objs}

INFRA: 4×c5n.large → ALB | ElastiCache Redis | RDS db.t3.medium | S3 us-west-2
Grafana: http://52.13.41.234:3000 | Prometheus: http://52.13.41.234:9090

Provide: 1) Direct answer with data evidence 2) System health observations
3) Priority-ordered optimization suggestions with expected impact
4) Any Terraform commands if infrastructure changes needed`;
  const pb=document.getElementById('pb');
  pb.textContent=p; pb.style.display='block';
  document.getElementById('cb').style.display='block';
}
function cp(){
  navigator.clipboard.writeText(document.getElementById('pb').textContent).then(()=>{
    const b=document.getElementById('cb');
    b.textContent='Copied!'; setTimeout(()=>b.textContent='Copy to Clipboard',2000);
  });
}
</script>
</body>
</html>"""

def render(m):
    html = PAGE
    subs = {
        "~~TIME~~": m["time"], "~~NODES~~": m["nodes"],
        "~~REQS~~": m["req_s"], "~~P95~~": m["p95_ms"], "~~P99~~": m["p99_ms"],
        "~~ERR~~": m["err_pct"], "~~CACHE~~": m["cache_hit"],
        "~~SEQ~~": m["seq_s"], "~~DB~~": m["db_conn"],
        "~~P95C~~": m["p99_class"],  # reuse p99 class for p95
        "~~P99C~~": m["p99_class"], "~~ERRC~~": m["err_class"],
        "~~CACHEC~~": m["cache_class"], "~~MDATA~~": m["metrics_json"],
    }
    for k, v in subs.items():
        html = html.replace(k, str(v))
    return html

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        body = render(gather()).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-Frame-Options", "ALLOWALL")
        self.send_header("Content-Security-Policy", "frame-ancestors *")
        self.end_headers()
        self.wfile.write(body)

if __name__ == "__main__":
    port = int(os.environ.get("PORT", "8089"))
    print(f"[devops-chat] :{port}")
    HTTPServer(("", port), Handler).serve_forever()
