# Terraform Usage Guide
## ChaosArena v1-album-store — 190/190 Infrastructure

---

## Directory Structure

```
terraform/
├── main.tf        — all AWS resources (SG, S3, ElastiCache, RDS, 4×EC2, ALB)
├── variables.tf   — tunable parameters with inline documentation
├── outputs.tf     — ALB URL, endpoints, ready-made ChaosArena submit command
├── deploy.sh      — build Go binary + SSM deploy to all 4 nodes
├── destroy.sh     — empty S3 bucket then terraform destroy
└── USAGE.md       — this file
```

---

## Next Lab Session — Full Setup in ~10 Minutes

### Step 1 — Initialize (first time only)

```bash
cd terraform/
terraform init
```

### Step 2 — Provision Infrastructure (~7 min)

```bash
terraform apply
```

This provisions in dependency order:
1. Security group + S3 bucket (seconds)
2. ElastiCache Redis + RDS PostgreSQL in parallel (~5 min)
3. 4× c5n.large EC2 nodes (boot, pull binary from S3, start service)
4. ALB + target group + listener + health checks

At the end, Terraform prints all endpoints and a ready-made submit command.

### Step 3 — Build and Deploy Binary

```bash
# From the server/ directory:
cd ../server && ../terraform/deploy.sh

# Or from anywhere:
./terraform/deploy.sh /path/to/server
```

`deploy.sh` reads endpoints from `terraform output`, builds a Linux/amd64 binary, uploads it to S3, and SSM-deploys to all 4 nodes in parallel. Prints ✅ or ❌ per node.

### Step 4 — Submit to ChaosArena

```bash
# Copy the exact command from terraform output:
terraform output -raw chaosarena_submit_cmd | bash

# Or manually:
curl -X POST http://chaosarena-alb-938452724.us-west-2.elb.amazonaws.com/submit \
  -H "Content-Type: application/json" \
  -d '{
    "email":    "wang.zongw@northeastern.edu",
    "nickname": "Zongwang Wang",
    "base_url": "'"$(terraform output -raw alb_url)"'",
    "contract": "v1-album-store"
  }'
```

**Submit immediately** after deploy — before any CPU credit depletion (c5n.large has dedicated network, no credits, so this is less critical than with t3).

---

## Teardown

```bash
cd terraform/
./destroy.sh
```

`destroy.sh` first empties the S3 bucket (Terraform `force_destroy` can be unreliable for non-empty buckets), then runs `terraform destroy -auto-approve`.

Manual equivalent if needed:
```bash
# Empty S3
aws s3 rm s3://$(terraform output -raw s3_bucket) --recursive

# Destroy everything
terraform destroy -auto-approve
```

---

## Key Variables

Defined in `variables.tf`. Override via `-var` flag or `terraform.tfvars`.

| Variable | Default | Notes |
|---|---|---|
| `instance_type` | `c5n.large` | **25 Gbps dedicated** — do not change. c5n.xlarge is blocked by lab policy. |
| `node_count` | `4` | 4 nodes × 25 Gbps = 100 Gbps total. Needed for S12=15/15 and S15=20/20. |
| `db_instance_class` | `db.t3.medium` | Faster than db.t3.micro for S11 concurrent album creates. |
| `redis_node_type` | `cache.t3.micro` | External ElastiCache — shared atomic INCR across all 4 nodes. |
| `db_password` | `AlbumStore2024!` | Change if desired; also update in `variables.tf` default. |
| `max_db_conns` | `25` | 4 nodes × 25 = 100 total; db.t3.medium supports ~170 max_connections. |

Example override:
```bash
terraform apply -var="node_count=2" -var="instance_type=m5.large"
```

---

## Useful Outputs

```bash
terraform output alb_url              # http://<alb-dns> — submit this to ChaosArena
terraform output rds_endpoint         # PostgreSQL host:port
terraform output redis_endpoint       # ElastiCache primary endpoint
terraform output instance_ids         # EC2 instance IDs (for SSM commands)
terraform output chaosarena_submit_cmd  # full curl command, ready to paste
```

---

## Checking Logs on a Node

```bash
INSTANCE_ID=$(terraform output -json instance_ids | python3 -c "import sys,json; print(json.load(sys.stdin)[0])")

aws ssm send-command \
  --instance-ids "$INSTANCE_ID" \
  --document-name "AWS-RunShellScript" \
  --parameters 'commands=["journalctl -u album-store -n 30 --no-pager"]' \
  --query 'Command.CommandId' --output text
```

---

## Architecture Summary

```
ChaosArena ALB (us-west-2)
        │
        ▼
  AWS ALB  (:80 → :8080)
   ├── c5n.large #1  ─┐
   ├── c5n.large #2   ├── ElastiCache Redis (shared seq INCR + read cache)
   ├── c5n.large #3   ├── RDS PostgreSQL db.t3.medium (albums + photos)
   └── c5n.large #4  ─┘
                           └── S3 (photo files, public-read)
```

**Why this achieves 190/190:**
- **c5n.large**: 25 Gbps dedicated network per node, no burst credits
- **4 nodes**: 100 Gbps total → S12 small files get high concurrency, S15 large files get bandwidth
- **ElastiCache**: shared Redis INCR ensures globally unique seq across all nodes
- **Tiered semaphore**: 64 goroutines for files < 10 MB, 8 goroutines for files ≥ 10 MB (per node)
- **db.t3.medium**: handles S11 concurrent creates without bottlenecking

---

## Cost Estimate

| Resource | Spec | Price/hr |
|---|---|---|
| 4× EC2 c5n.large | 2 vCPU, 5.5 GB RAM, 25 Gbps | 4 × $0.108 = $0.432 |
| RDS db.t3.medium | PostgreSQL 17, 20 GB gp2 | ~$0.068 |
| ElastiCache cache.t3.micro | Redis 7 | ~$0.017 |
| ALB | ~1 LCU for load tests | ~$0.018 |
| S3 | minimal storage + transfer | ~$0.001 |
| **Total** | | **~$0.54/hr** |

With a $40 lab budget: ~74 hours of runtime. One full session (provision → test → destroy) runs about 30–60 minutes = ~$0.50.
