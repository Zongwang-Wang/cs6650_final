###############################################################################
# variables.tf - All input variables for the cs6650-final monitoring project
###############################################################################

# ---------- General ----------

variable "aws_region" {
  description = "AWS region to deploy into"
  type        = string
  default     = "us-west-2"
}

variable "project_name" {
  description = "Project identifier used for naming and tagging"
  type        = string
  default     = "cs6650-final"
}

# ---------- Networking ----------

variable "vpc_cidr" {
  description = "CIDR block for the VPC"
  type        = string
  default     = "10.0.0.0/16"
}

variable "public_subnet_cidrs" {
  description = "CIDR blocks for the two public subnets"
  type        = list(string)
  default     = ["10.0.1.0/24", "10.0.2.0/24"]
}

variable "availability_zones" {
  description = "AZs for the public subnets"
  type        = list(string)
  default     = ["us-west-2a", "us-west-2b"]
}

# ---------- ECS / Fargate ----------

variable "cart_api_image" {
  description = "Docker image for the cart-api task (placeholder until ECR image is ready)"
  type        = string
  default     = "public.ecr.aws/nginx/nginx:latest"
}

variable "cart_api_cpu" {
  description = "CPU units for the cart-api task"
  type        = number
  default     = 256
}

variable "cart_api_memory" {
  description = "Memory (MiB) for the cart-api task"
  type        = number
  default     = 512
}

variable "cart_api_container_port" {
  description = "Container port exposed by the cart-api"
  type        = number
  default     = 8080
}

# ---------- Scaling mode ----------

variable "scaling_mode" {
  description = "Which auto-scaling approach is active: 'static' or 'ai'"
  type        = string
  default     = "static"

  validation {
    condition     = contains(["static", "ai"], var.scaling_mode)
    error_message = "scaling_mode must be either 'static' or 'ai'."
  }
}

# --- Static scaling parameters ---

variable "static_desired_count" {
  description = "Desired task count when using static (CPU target-tracking) scaling"
  type        = number
  default     = 4
}

variable "static_min_count" {
  description = "Minimum task count for static auto-scaling"
  type        = number
  default     = 1
}

variable "static_max_count" {
  description = "Maximum task count for static auto-scaling"
  type        = number
  default     = 8
}

variable "static_cpu_target" {
  description = "CPU utilization target (%) for static target-tracking policy"
  type        = number
  default     = 70
}

variable "static_scale_in_cooldown" {
  description = "Scale-in cooldown (seconds) for static policy"
  type        = number
  default     = 300
}

variable "static_scale_out_cooldown" {
  description = "Scale-out cooldown (seconds) for static policy"
  type        = number
  default     = 300
}

# --- AI-driven scaling parameters ---

variable "ai_desired_count" {
  description = "Desired task count set directly by the AI agent (used when scaling_mode = 'ai')"
  type        = number
  default     = 4
}

variable "ai_min_count" {
  description = "Minimum task count for AI-driven scaling"
  type        = number
  default     = 1
}

variable "ai_max_count" {
  description = "Maximum task count for AI-driven scaling"
  type        = number
  default     = 8
}

# ---------- Kafka / MSK ----------

variable "kafka_instance_type" {
  description = "Instance type for MSK broker nodes (or Fargate CPU/mem for self-hosted)"
  type        = string
  default     = "kafka.t3.small"
}

variable "kafka_ebs_volume_size" {
  description = "EBS volume size (GiB) per MSK broker"
  type        = number
  default     = 10
}
