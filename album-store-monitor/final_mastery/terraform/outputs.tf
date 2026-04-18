output "alb_url" {
  description = "Base URL to submit to ChaosArena"
  value       = "http://${aws_lb.album_store.dns_name}"
}

output "alb_dns" {
  description = "ALB DNS name"
  value       = aws_lb.album_store.dns_name
}

output "s3_bucket" {
  description = "S3 bucket name for photo storage"
  value       = aws_s3_bucket.photos.id
}

output "rds_endpoint" {
  description = "RDS PostgreSQL endpoint"
  value       = aws_db_instance.postgres.endpoint
}

output "redis_endpoint" {
  description = "ElastiCache Redis primary endpoint"
  value       = aws_elasticache_replication_group.redis.primary_endpoint_address
}

output "instance_ids" {
  description = "EC2 instance IDs (for SSM deploy)"
  value       = aws_instance.nodes[*].id
}

output "instance_public_ips" {
  description = "EC2 public IPs"
  value       = aws_instance.nodes[*].public_ip
}

output "chaosarena_submit_cmd" {
  description = "Ready-to-run ChaosArena submission command"
  value = <<-EOT
    curl -X POST http://chaosarena-alb-938452724.us-west-2.elb.amazonaws.com/submit \
      -H "Content-Type: application/json" \
      -d '{
        "email":    "wang.zongw@northeastern.edu",
        "nickname": "Zongwang Wang",
        "base_url": "http://${aws_lb.album_store.dns_name}",
        "contract": "v1-album-store"
      }'
  EOT
}
