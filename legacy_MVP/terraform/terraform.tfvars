###############################################################################
# terraform.tfvars - Default variable values
#
# The AI scaling agent modifies ai_desired_count in this file directly.
# Switch scaling_mode between "static" and "ai" to compare approaches.
###############################################################################

aws_region   = "us-west-2"
project_name = "cs6650-final"

# ---------- Networking ----------
vpc_cidr            = "10.0.0.0/16"
public_subnet_cidrs = ["10.0.1.0/24", "10.0.2.0/24"]
availability_zones  = ["us-west-2a", "us-west-2b"]

# ---------- ECS / Cart API ----------
cart_api_image          = "public.ecr.aws/nginx/nginx:latest"
cart_api_cpu            = 256
cart_api_memory         = 512
cart_api_container_port = 8080

# ---------- Scaling mode: "static" or "ai" ----------
scaling_mode = "static"

# --- Static (CPU target-tracking) scaling ---
static_desired_count     = 4
static_min_count         = 1
static_max_count         = 8
static_cpu_target        = 70
static_scale_in_cooldown  = 300
static_scale_out_cooldown = 300

# --- AI-driven scaling (agent updates ai_desired_count) ---
ai_desired_count = 4
ai_min_count     = 1
ai_max_count     = 8

# ---------- Kafka ----------
kafka_instance_type   = "kafka.t3.small"
kafka_ebs_volume_size = 10
