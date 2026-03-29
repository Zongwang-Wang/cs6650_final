###############################################################################
# outputs.tf - Exported values
###############################################################################

output "alb_dns_name" {
  description = "DNS name of the Application Load Balancer for cart-api"
  value       = aws_lb.cart_api.dns_name
}

output "kafka_nlb_dns_name" {
  description = "Internal NLB DNS name for Kafka"
  value       = aws_lb.kafka.dns_name
}

output "ecs_cluster_name" {
  description = "Name of the ECS cluster"
  value       = aws_ecs_cluster.main.name
}

output "cart_api_service_name" {
  description = "Name of the cart-api ECS service"
  value       = aws_ecs_service.cart_api.name
}

output "scaling_mode" {
  description = "Currently active scaling mode (static or ai)"
  value       = var.scaling_mode
}

output "ecr_cart_api" {
  description = "ECR repository URL for cart-api"
  value       = aws_ecr_repository.cart_api.repository_url
}

output "ecr_analytics" {
  description = "ECR repository URL for analytics"
  value       = aws_ecr_repository.analytics.repository_url
}

output "prometheus_url" {
  description = "Public URL for Prometheus (for local Grafana)"
  value       = "http://${aws_lb.prometheus.dns_name}:9090"
}

output "ecr_prometheus" {
  description = "ECR repository URL for prometheus"
  value       = aws_ecr_repository.prometheus.repository_url
}

output "ecr_alert" {
  description = "ECR repository URL for alert"
  value       = aws_ecr_repository.alert.repository_url
}
