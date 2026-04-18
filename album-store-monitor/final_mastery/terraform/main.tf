terraform {
  required_version = ">= 1.6"
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

# ─── Data sources ──────────────────────────────────────────────────────────────

data "aws_caller_identity" "current" {}

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

# Latest Amazon Linux 2023 AMI (x86_64)
data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-2023*-x86_64"]
  }
  filter {
    name   = "state"
    values = ["available"]
  }
}

# ─── Security Group ────────────────────────────────────────────────────────────

resource "aws_security_group" "album_store" {
  name        = "album-store-sg"
  description = "Album store: HTTP, SSH, and internal traffic"
  vpc_id      = data.aws_vpc.default.id

  # Public HTTP for ALB and direct testing
  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  # Internal: Redis + PostgreSQL between nodes (self-referencing)
  ingress {
    from_port = 0
    to_port   = 0
    protocol  = "-1"
    self      = true
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "album-store-sg" }
}

# ─── S3 Bucket ─────────────────────────────────────────────────────────────────

locals {
  bucket_name = "album-store-${data.aws_caller_identity.current.account_id}-${var.aws_region}"
}

resource "aws_s3_bucket" "photos" {
  bucket        = local.bucket_name
  force_destroy = true  # allows destroy even when bucket has objects

  tags = { Name = "album-store-photos" }
}

resource "aws_s3_bucket_public_access_block" "photos" {
  bucket                  = aws_s3_bucket.photos.id
  block_public_acls       = false
  ignore_public_acls      = false
  block_public_policy     = false
  restrict_public_buckets = false
}

resource "aws_s3_bucket_policy" "photos_public_read" {
  bucket = aws_s3_bucket.photos.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "PublicRead"
      Effect    = "Allow"
      Principal = "*"
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.photos.arn}/*"
    }]
  })

  depends_on = [aws_s3_bucket_public_access_block.photos]
}

# ─── ElastiCache (Redis) ───────────────────────────────────────────────────────

resource "aws_elasticache_subnet_group" "album_store" {
  name       = "album-store-redis-subnets"
  subnet_ids = data.aws_subnets.default.ids
}

resource "aws_elasticache_replication_group" "redis" {
  replication_group_id = "album-store-redis"
  description          = "Album store shared Redis - seq counters + read cache"

  engine               = "redis"
  engine_version       = "7.1"
  node_type            = var.redis_node_type   # cache.t3.micro
  num_cache_clusters   = 1
  port                 = 6379

  subnet_group_name    = aws_elasticache_subnet_group.album_store.name
  security_group_ids   = [aws_security_group.album_store.id]

  # No auth — VPC security group provides access control
  transit_encryption_enabled = false
  at_rest_encryption_enabled = false

  tags = { Name = "album-store-redis" }
}

# ─── RDS PostgreSQL ────────────────────────────────────────────────────────────

resource "aws_db_subnet_group" "album_store" {
  name       = "album-store-db-subnets"
  subnet_ids = data.aws_subnets.default.ids
  tags       = { Name = "album-store-db-subnets" }
}

resource "aws_db_instance" "postgres" {
  identifier = "album-store-db"

  engine         = "postgres"
  engine_version = "17.2"
  instance_class = var.db_instance_class   # db.t3.medium

  db_name  = "albumstore"
  username = "albumadmin"
  password = var.db_password

  allocated_storage       = 20
  storage_type            = "gp2"
  publicly_accessible     = false
  multi_az                = false
  backup_retention_period = 0
  deletion_protection     = false
  skip_final_snapshot     = true

  db_subnet_group_name   = aws_db_subnet_group.album_store.name
  vpc_security_group_ids = [aws_security_group.album_store.id]

  tags = { Name = "album-store-db" }
}

# ─── EC2 — User Data ──────────────────────────────────────────────────────────

locals {
  user_data = <<-EOF
    #!/bin/bash
    exec > /var/log/album-store-setup.log 2>&1
    yum update -y
    yum install -y redis6
    systemctl enable redis6
    systemctl start redis6

    mkdir -p /home/ec2-user/album-store

    cat > /etc/album-store.env << 'ENVEOF'
    DATABASE_URL=postgres://${var.db_username}:${var.db_password}@${aws_db_instance.postgres.endpoint}/albumstore
    REDIS_URL=redis://${aws_elasticache_replication_group.redis.primary_endpoint_address}:6379/0
    S3_BUCKET=${local.bucket_name}
    AWS_REGION=${var.aws_region}
    PORT=8080
    WORKER_COUNT=${var.worker_count}
    MAX_DB_CONNS=${var.max_db_conns}
    ENVEOF

    cat > /etc/systemd/system/album-store.service << 'SVCEOF'
    [Unit]
    Description=Album Store API Server
    After=network.target

    [Service]
    Type=simple
    User=ec2-user
    WorkingDirectory=/home/ec2-user/album-store
    EnvironmentFile=/etc/album-store.env
    ExecStart=/home/ec2-user/album-store/server
    Restart=always
    RestartSec=2
    LimitNOFILE=65536

    [Install]
    WantedBy=multi-user.target
    SVCEOF

    systemctl daemon-reload

    # Download binary from S3 (uploaded separately via deploy.sh)
    aws s3 cp s3://${local.bucket_name}/deploy/album-store-server \
      /home/ec2-user/album-store/server --no-progress || true
    chmod +x /home/ec2-user/album-store/server
    chown -R ec2-user:ec2-user /home/ec2-user/album-store

    # Start if binary exists, otherwise wait for deploy.sh
    [ -f /home/ec2-user/album-store/server ] && systemctl start album-store
    echo "Setup complete"
  EOF
}

# ─── EC2 Instances (4× c5n.large) ─────────────────────────────────────────────

resource "aws_instance" "nodes" {
  count = var.node_count   # 4

  ami           = data.aws_ami.al2023.id
  instance_type = var.instance_type   # c5n.large (25 Gbps dedicated)

  vpc_security_group_ids = [aws_security_group.album_store.id]
  iam_instance_profile   = "LabInstanceProfile"
  monitoring             = true

  user_data = base64encode(local.user_data)

  tags = { Name = "album-store-node-${count.index + 1}" }

  # Ensure RDS and ElastiCache exist before instances boot
  depends_on = [
    aws_db_instance.postgres,
    aws_elasticache_replication_group.redis,
    aws_s3_bucket_policy.photos_public_read,
  ]
}

# ─── Application Load Balancer ─────────────────────────────────────────────────

resource "aws_lb" "album_store" {
  name               = "album-store-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.album_store.id]
  subnets            = data.aws_subnets.default.ids

  idle_timeout = 300

  tags = { Name = "album-store-alb" }
}

resource "aws_lb_target_group" "nodes" {
  name     = "album-store-tg"
  port     = 8080
  protocol = "HTTP"
  vpc_id   = data.aws_vpc.default.id

  health_check {
    path                = "/health"
    interval            = 10
    healthy_threshold   = 2
    unhealthy_threshold = 2
    timeout             = 5
    matcher             = "200"
  }

  tags = { Name = "album-store-tg" }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.album_store.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.nodes.arn
  }
}

resource "aws_lb_target_group_attachment" "nodes" {
  count            = var.node_count
  target_group_arn = aws_lb_target_group.nodes.arn
  target_id        = aws_instance.nodes[count.index].id
  port             = 8080
}
