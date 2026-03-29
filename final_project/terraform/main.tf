###############################################################################
# main.tf - Provider, VPC, ALB, ECS Fargate, Auto-Scaling, Kafka (NLB),
#           ECR, CloudWatch log groups, security groups.
#
# Uses pre-existing LabRole for ECS task execution/task roles.
# Uses an internal NLB for Kafka service discovery (no Cloud Map needed).
###############################################################################

# ========================== Provider & Locals ================================

terraform {
  required_version = ">= 1.3"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

data "aws_caller_identity" "current" {}

locals {
  tags = {
    Project   = var.project_name
    ManagedBy = "terraform"
  }

  account_id = data.aws_caller_identity.current.account_id
  ecr_base   = "${local.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com"
  lab_role   = "arn:aws:iam::${local.account_id}:role/LabRole"

  # Pick counts based on active scaling mode
  desired_count = var.scaling_mode == "ai" ? var.ai_desired_count : var.static_desired_count
  min_count     = var.scaling_mode == "ai" ? var.ai_min_count : var.static_min_count
  max_count     = var.scaling_mode == "ai" ? var.ai_max_count : var.static_max_count
}

# ============================== ECR ==========================================

resource "aws_ecr_repository" "cart_api" {
  name                 = "${var.project_name}/cart-api"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
  tags                 = local.tags
}

resource "aws_ecr_repository" "analytics" {
  name                 = "${var.project_name}/analytics"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
  tags                 = local.tags
}

resource "aws_ecr_repository" "alert" {
  name                 = "${var.project_name}/alert"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
  tags                 = local.tags
}

# ============================== VPC ==========================================

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(local.tags, { Name = "${var.project_name}-vpc" })
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id

  tags = merge(local.tags, { Name = "${var.project_name}-igw" })
}

resource "aws_subnet" "public" {
  count = length(var.public_subnet_cidrs)

  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.public_subnet_cidrs[count.index]
  availability_zone       = var.availability_zones[count.index]
  map_public_ip_on_launch = true

  tags = merge(local.tags, {
    Name = "${var.project_name}-public-${var.availability_zones[count.index]}"
  })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }

  tags = merge(local.tags, { Name = "${var.project_name}-public-rt" })
}

resource "aws_route_table_association" "public" {
  count = length(aws_subnet.public)

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# ========================= Security Groups ===================================

resource "aws_security_group" "alb" {
  name        = "${var.project_name}-alb-sg"
  description = "Allow HTTP inbound to ALB"
  vpc_id      = aws_vpc.main.id

  ingress {
    description = "HTTP from anywhere"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.tags, { Name = "${var.project_name}-alb-sg" })
}

# Shared SG for all ECS tasks — they can talk to each other and to Kafka
resource "aws_security_group" "ecs_tasks" {
  name        = "${var.project_name}-ecs-tasks-sg"
  description = "Allow traffic between ECS tasks and from ALB"
  vpc_id      = aws_vpc.main.id

  # From ALB on cart-api port
  ingress {
    description     = "From ALB on container port"
    from_port       = var.cart_api_container_port
    to_port         = var.cart_api_container_port
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  # Allow all traffic between tasks in this SG (Kafka, inter-service)
  ingress {
    description = "Inter-task communication"
    from_port   = 0
    to_port     = 65535
    protocol    = "tcp"
    self        = true
  }

  # NLB passes through client IPs, so we need to allow the whole VPC CIDR
  # for Kafka and Prometheus NLB traffic
  ingress {
    description = "VPC internal (for NLB pass-through)"
    from_port   = 9092
    to_port     = 9092
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }

  ingress {
    description = "Prometheus NLB pass-through"
    from_port   = 9090
    to_port     = 9090
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.tags, { Name = "${var.project_name}-ecs-tasks-sg" })
}

# ====================== Application Load Balancer (cart-api) =================

resource "aws_lb" "cart_api" {
  name               = "${var.project_name}-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = aws_subnet.public[*].id

  tags = merge(local.tags, { Name = "${var.project_name}-alb" })
}

resource "aws_lb_target_group" "cart_api" {
  name        = "${var.project_name}-cart-api-tg"
  port        = var.cart_api_container_port
  protocol    = "HTTP"
  vpc_id      = aws_vpc.main.id
  target_type = "ip"

  health_check {
    path                = "/health"
    port                = "traffic-port"
    protocol            = "HTTP"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 30
    matcher             = "200"
  }

  tags = merge(local.tags, { Name = "${var.project_name}-cart-api-tg" })
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.cart_api.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.cart_api.arn
  }

  tags = local.tags
}

# ====================== Internal NLB for Kafka ===============================

resource "aws_lb" "kafka" {
  name               = "${var.project_name}-kafka-nlb"
  internal           = true
  load_balancer_type = "network"
  subnets            = aws_subnet.public[*].id

  tags = merge(local.tags, { Name = "${var.project_name}-kafka-nlb" })
}

resource "aws_lb_target_group" "kafka" {
  name        = "${var.project_name}-kafka-tg"
  port        = 9092
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "ip"

  health_check {
    protocol            = "TCP"
    port                = "traffic-port"
    healthy_threshold   = 2
    unhealthy_threshold = 2
    interval            = 10
  }

  tags = local.tags
}

resource "aws_lb_listener" "kafka" {
  load_balancer_arn = aws_lb.kafka.arn
  port              = 9092
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.kafka.arn
  }

  tags = local.tags
}

# ======================== CloudWatch Log Groups ==============================

resource "aws_cloudwatch_log_group" "cart_api" {
  name              = "/ecs/${var.project_name}/cart-api"
  retention_in_days = 7
  tags              = local.tags
}

resource "aws_cloudwatch_log_group" "analytics" {
  name              = "/ecs/${var.project_name}/analytics"
  retention_in_days = 7
  tags              = local.tags
}

resource "aws_cloudwatch_log_group" "alert" {
  name              = "/ecs/${var.project_name}/alert"
  retention_in_days = 7
  tags              = local.tags
}

resource "aws_cloudwatch_log_group" "kafka" {
  name              = "/ecs/${var.project_name}/kafka"
  retention_in_days = 7
  tags              = local.tags
}

# =========================== ECS Cluster =====================================

resource "aws_ecs_cluster" "main" {
  name = "${var.project_name}-cluster"

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  tags = local.tags
}

# ====================== Kafka (Single-broker KRaft on ECS) ===================

resource "aws_ecs_task_definition" "kafka" {
  family                   = "${var.project_name}-kafka"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = 1024
  memory                   = 2048
  execution_role_arn       = local.lab_role
  task_role_arn            = local.lab_role

  container_definitions = jsonencode([
    {
      name      = "kafka"
      image     = "confluentinc/cp-kafka:7.5.0"
      essential = true

      portMappings = [
        { containerPort = 9092, protocol = "tcp" },
        { containerPort = 9093, protocol = "tcp" }
      ]

      environment = [
        { name = "KAFKA_NODE_ID", value = "1" },
        { name = "KAFKA_PROCESS_ROLES", value = "broker,controller" },
        { name = "KAFKA_LISTENERS", value = "PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093" },
        { name = "KAFKA_ADVERTISED_LISTENERS", value = "PLAINTEXT://${aws_lb.kafka.dns_name}:9092" },
        { name = "KAFKA_CONTROLLER_LISTENER_NAMES", value = "CONTROLLER" },
        { name = "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP", value = "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT" },
        { name = "KAFKA_CONTROLLER_QUORUM_VOTERS", value = "1@localhost:9093" },
        { name = "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR", value = "1" },
        { name = "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR", value = "1" },
        { name = "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR", value = "1" },
        { name = "KAFKA_AUTO_CREATE_TOPICS_ENABLE", value = "true" },
        { name = "CLUSTER_ID", value = "MkU3OEVBNTcwNTJENDM2Qk" }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.kafka.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "kafka"
        }
      }
    }
  ])

  tags = local.tags
}

resource "aws_ecs_service" "kafka" {
  name            = "${var.project_name}-kafka"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.kafka.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.kafka.arn
    container_name   = "kafka"
    container_port   = 9092
  }

  depends_on = [aws_lb_listener.kafka]

  tags = local.tags
}

# ====================== Cart API ===========================================

resource "aws_ecs_task_definition" "cart_api" {
  family                   = "${var.project_name}-cart-api"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = var.cart_api_cpu
  memory                   = var.cart_api_memory
  execution_role_arn       = local.lab_role
  task_role_arn            = local.lab_role

  container_definitions = jsonencode([
    {
      name      = "cart-api"
      image     = "${aws_ecr_repository.cart_api.repository_url}:latest"
      essential = true

      portMappings = [
        {
          containerPort = var.cart_api_container_port
          protocol      = "tcp"
        }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.cart_api.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "cart-api"
        }
      }

      environment = [
        { name = "PORT", value = tostring(var.cart_api_container_port) },
        { name = "KAFKA_BROKERS", value = "${aws_lb.kafka.dns_name}:9092" },
        { name = "KAFKA_TOPIC", value = "metrics-topic" }
      ]
    }
  ])

  tags = local.tags
}

resource "aws_ecs_service" "cart_api" {
  name            = "${var.project_name}-cart-api"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.cart_api.arn
  desired_count   = local.desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.cart_api.arn
    container_name   = "cart-api"
    container_port   = var.cart_api_container_port
  }

  depends_on = [aws_lb_listener.http, aws_ecs_service.kafka]

  tags = local.tags
}

# ====================== Analytics Service ====================================

resource "aws_ecs_task_definition" "analytics" {
  family                   = "${var.project_name}-analytics"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = local.lab_role
  task_role_arn            = local.lab_role

  container_definitions = jsonencode([
    {
      name      = "analytics"
      image     = "${aws_ecr_repository.analytics.repository_url}:latest"
      essential = true

      portMappings = [
        { containerPort = 8081, protocol = "tcp" }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.analytics.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "analytics"
        }
      }

      environment = [
        { name = "PORT", value = "8081" },
        { name = "KAFKA_BROKERS", value = "${aws_lb.kafka.dns_name}:9092" },
        { name = "KAFKA_CONSUMER_TOPIC", value = "metrics-topic" },
        { name = "KAFKA_PRODUCER_TOPIC", value = "analytics-output-topic" },
        { name = "CONSUMER_GROUP", value = "analytics-group" },
        { name = "DYNAMODB_TABLE", value = aws_dynamodb_table.metrics.name },
        { name = "AWS_REGION", value = var.aws_region }
      ]
    }
  ])

  tags = local.tags
}

resource "aws_ecs_service" "analytics" {
  name            = "${var.project_name}-analytics"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.analytics.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = true
  }

  depends_on = [aws_ecs_service.kafka]

  tags = local.tags
}

# ====================== Alert Service ========================================

resource "aws_ecs_task_definition" "alert" {
  family                   = "${var.project_name}-alert"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = local.lab_role
  task_role_arn            = local.lab_role

  container_definitions = jsonencode([
    {
      name      = "alert"
      image     = "${aws_ecr_repository.alert.repository_url}:latest"
      essential = true

      portMappings = [
        { containerPort = 8082, protocol = "tcp" }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.alert.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "alert"
        }
      }

      environment = [
        { name = "PORT", value = "8082" },
        { name = "KAFKA_BROKERS", value = "${aws_lb.kafka.dns_name}:9092" },
        { name = "KAFKA_TOPIC", value = "metrics-topic" },
        { name = "CONSUMER_GROUP", value = "alert-group" },
        { name = "SNS_TOPIC_ARN", value = aws_sns_topic.alerts.arn },
        { name = "AWS_REGION", value = var.aws_region }
      ]
    }
  ])

  tags = local.tags
}

resource "aws_ecs_service" "alert" {
  name            = "${var.project_name}-alert"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.alert.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = true
  }

  depends_on = [aws_ecs_service.kafka]

  tags = local.tags
}

# ====================== DynamoDB (Analytics Persistence) =====================

resource "aws_dynamodb_table" "metrics" {
  name         = "${var.project_name}-metrics"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "timestamp"

  attribute {
    name = "timestamp"
    type = "S"
  }

  tags = local.tags
}

# ====================== SNS (Alert Notifications) ============================

resource "aws_sns_topic" "alerts" {
  name = "${var.project_name}-alerts"
  tags = local.tags
}

# ====================== Prometheus (Monitoring) ==============================

resource "aws_ecr_repository" "prometheus" {
  name                 = "${var.project_name}/prometheus"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
  tags                 = local.tags
}

resource "aws_cloudwatch_log_group" "prometheus" {
  name              = "/ecs/${var.project_name}/prometheus"
  retention_in_days = 7
  tags              = local.tags
}

# Public NLB so local Grafana can reach Prometheus
resource "aws_lb" "prometheus" {
  name               = "${var.project_name}-prom-nlb"
  internal           = false
  load_balancer_type = "network"
  subnets            = aws_subnet.public[*].id

  tags = merge(local.tags, { Name = "${var.project_name}-prom-nlb" })
}

resource "aws_lb_target_group" "prometheus" {
  name        = "${var.project_name}-prom-tg"
  port        = 9090
  protocol    = "TCP"
  vpc_id      = aws_vpc.main.id
  target_type = "ip"

  health_check {
    protocol            = "TCP"
    port                = "traffic-port"
    healthy_threshold   = 2
    unhealthy_threshold = 2
    interval            = 10
  }

  tags = local.tags
}

resource "aws_lb_listener" "prometheus" {
  load_balancer_arn = aws_lb.prometheus.arn
  port              = 9090
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.prometheus.arn
  }

  tags = local.tags
}

resource "aws_ecs_task_definition" "prometheus" {
  family                   = "${var.project_name}-prometheus"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = 512
  memory                   = 1024
  execution_role_arn       = local.lab_role
  task_role_arn            = local.lab_role

  container_definitions = jsonencode([
    {
      name      = "prometheus"
      image     = "${aws_ecr_repository.prometheus.repository_url}:v3"
      essential = true

      portMappings = [
        { containerPort = 9090, protocol = "tcp" }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.prometheus.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "prometheus"
        }
      }
    }
  ])

  tags = local.tags
}

resource "aws_ecs_service" "prometheus" {
  name            = "${var.project_name}-prometheus"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.prometheus.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.prometheus.arn
    container_name   = "prometheus"
    container_port   = 9090
  }

  depends_on = [aws_lb_listener.prometheus]

  tags = local.tags
}

# ======================== Auto-Scaling =======================================

resource "aws_appautoscaling_target" "cart_api" {
  count = var.scaling_mode == "static" ? 1 : 0

  max_capacity       = local.max_count
  min_capacity       = local.min_count
  resource_id        = "service/${aws_ecs_cluster.main.name}/${aws_ecs_service.cart_api.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "cart_api_cpu" {
  count = var.scaling_mode == "static" ? 1 : 0

  name               = "${var.project_name}-cart-api-cpu-scaling"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.cart_api[0].resource_id
  scalable_dimension = aws_appautoscaling_target.cart_api[0].scalable_dimension
  service_namespace  = aws_appautoscaling_target.cart_api[0].service_namespace

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }

    target_value       = var.static_cpu_target
    scale_in_cooldown  = var.static_scale_in_cooldown
    scale_out_cooldown = var.static_scale_out_cooldown
  }
}
