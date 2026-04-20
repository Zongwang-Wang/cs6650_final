variable "aws_region" {
  description = "AWS region — must match ChaosArena's region (us-west-2) for low latency"
  type        = string
  default     = "us-west-2"
}

variable "instance_type" {
  description = <<-EOT
    EC2 instance type for API nodes.
    c5n.large = 2 vCPU, 5.5 GB RAM, 25 Gbps dedicated network (BEST for load tests)
    m5.large  = 2 vCPU, 8 GB RAM, 10 Gbps dedicated (fallback if c5n blocked by policy)
    t3.large  = 2 vCPU, 8 GB RAM, 5 Gbps BURST — depletes credits after 3-4 runs. Avoid.
  EOT
  type    = string
  default = "c5n.large"
}

variable "node_count" {
  description = <<-EOT
    Number of API server nodes behind the ALB.
    4 nodes × 25 Gbps (c5n.large) = 100 Gbps total — required for S12=15/15 and S15=20/20.
    1 node achieves ~181/190 max. 4 nodes achieves 190/190.
  EOT
  type    = number
  default = 4
}

variable "db_instance_class" {
  description = <<-EOT
    RDS instance class.
    db.t3.medium — 2 vCPU, 4 GB RAM. Required for S11 concurrent creates at full score.
    db.t3.micro  — 1 vCPU, 1 GB RAM. Sufficient for correctness but weaker under load.
  EOT
  type    = string
  default = "db.t3.medium"
}

variable "db_username" {
  description = "RDS master username"
  type        = string
  default     = "albumadmin"
}

variable "db_password" {
  description = "RDS master password"
  type        = string
  sensitive   = true
  default     = "AlbumStore2024!"
}

variable "redis_node_type" {
  description = <<-EOT
    ElastiCache node type.
    cache.t3.micro — shared seq INCR across all nodes. External ElastiCache eliminates
    the EC2 Redis bind/port issue when running multiple instances.
  EOT
  type    = string
  default = "cache.t3.micro"
}

variable "worker_count" {
  description = <<-EOT
    Number of goroutines in the worker pool (per node).
    Not used with tiered semaphore approach — kept for env compat. Ignored in handler.
  EOT
  type    = number
  default = 32
}

variable "max_db_conns" {
  description = <<-EOT
    Max PostgreSQL connections per node.
    4 nodes × 25 = 100 total. db.t3.medium supports ~170 max_connections.
    Keep headroom for RDS internal connections.
  EOT
  type    = number
  default = 25
}
