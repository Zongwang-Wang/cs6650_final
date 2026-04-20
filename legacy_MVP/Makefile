.PHONY: help up down kafka-up kafka-down topics build test lint locust locust-steady locust-spike tf-init tf-plan tf-apply tf-apply-ai logs

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ── Docker Compose ─────────────────────────────────────────────
up: ## Start all services (Kafka + cart-api + monitoring)
	docker compose up -d --build

down: ## Stop all services
	docker compose down

kafka-up: ## Start only the Kafka cluster
	docker compose -f docker-compose.kafka.yml up -d

kafka-down: ## Stop the Kafka cluster
	docker compose -f docker-compose.kafka.yml down

topics: ## Create Kafka topics manually
	bash scripts/setup-kafka-topics.sh

logs: ## Tail logs for all services
	docker compose logs -f

logs-cart: ## Tail cart-api logs
	docker compose logs -f cart-api

# ── Build & Test ───────────────────────────────────────────────
build: ## Build cart-api binary locally
	cd services/cart-api && go build -o ../../bin/cart-api .

test: ## Run all Go tests
	cd services/cart-api && go test -v ./...

lint: ## Run Go vet on all services
	cd services/cart-api && go vet ./...

# ── Load Testing ───────────────────────────────────────────────
locust: ## Open Locust web UI (default: http://localhost:8089)
	cd loadtest && pip install -q -r requirements.txt && locust -f locustfile.py --host=http://localhost:8080

locust-steady: ## Run Locust headless: 200 users, steady load, 2 min
	cd loadtest && locust -f locustfile.py SteadyUser --host=http://localhost:8080 --users 200 --spawn-rate 20 --run-time 2m --headless

locust-ramp: ## Run Locust headless: ramp 10→1000 users over 5 min
	cd loadtest && locust -f locustfile.py RampUser --host=http://localhost:8080 --users 1000 --spawn-rate 3 --run-time 5m --headless

locust-spike: ## Run Locust headless: spike profile, 500 users, 3 min
	cd loadtest && locust -f locustfile.py SpikeUser --host=http://localhost:8080 --users 500 --spawn-rate 50 --run-time 3m --headless

# ── Terraform ──────────────────────────────────────────────────
tf-init: ## Initialize Terraform
	cd terraform && terraform init

tf-plan: ## Plan Terraform changes (static mode)
	cd terraform && terraform plan

tf-apply: ## Apply Terraform (static scaling mode)
	cd terraform && terraform apply -auto-approve

tf-apply-ai: ## Apply Terraform with AI scaling mode
	cd terraform && terraform apply -auto-approve -var="scaling_mode=ai"

tf-destroy: ## Destroy all Terraform resources
	cd terraform && terraform destroy -auto-approve
