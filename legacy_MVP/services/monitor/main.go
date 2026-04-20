package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// AggregatedMetric mirrors the struct that analytics writes to DynamoDB.
// The monitor reads these records to build the context it passes to Claude Code.
// Note: the monitor does NOT consume Kafka directly — it uses DynamoDB as a
// decoupled data store, which means it can run locally without joining the Kafka cluster.
type AggregatedMetric struct {
	Timestamp         string  `json:"timestamp" dynamodbav:"timestamp"`
	WindowSeconds     int     `json:"window_seconds" dynamodbav:"window_seconds"`
	AvgCPUPercent     float64 `json:"avg_cpu_percent" dynamodbav:"avg_cpu_percent"`
	MaxCPUPercent     float64 `json:"max_cpu_percent" dynamodbav:"max_cpu_percent"`
	AvgLatencyMs      float64 `json:"avg_latency_ms" dynamodbav:"avg_latency_ms"`
	P99LatencyMs      float64 `json:"p99_latency_ms" dynamodbav:"p99_latency_ms"`
	RequestRatePerSec float64 `json:"request_rate_per_sec" dynamodbav:"request_rate_per_sec"`
	ErrorRatePercent  float64 `json:"error_rate_percent" dynamodbav:"error_rate_percent"`
	Trend             string  `json:"trend" dynamodbav:"trend"`
	InstanceCount     int     `json:"instance_count" dynamodbav:"instance_count"`
}

type AlertCondition struct {
	Type    string
	Message string
}

type MonitorState struct {
	mu              sync.Mutex
	history         []AggregatedMetric
	maxHistory      int
	lastInvocation  time.Time
	cooldownSeconds int
	lastTimestamp   string // track last seen DynamoDB item
}

func main() {
	tableName := flag.String("table", "cs6650-final-metrics", "DynamoDB table name")
	terraformDir := flag.String("terraform-dir", "", "Path to terraform directory")
	cooldown := flag.Int("cooldown", 120, "Cooldown seconds between Claude Code invocations")
	pollInterval := flag.Int("poll", 10, "Seconds between DynamoDB polls")
	dryRun := flag.Bool("dry-run", false, "If true, only print the prompt without invoking Claude Code")
	flag.Parse()

	if *terraformDir == "" {
		*terraformDir = findTerraformDir()
	}

	// Initialize AWS SDK
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion("us-west-2"))
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}
	ddb := dynamodb.NewFromConfig(cfg)

	state := &MonitorState{
		maxHistory:      10,
		cooldownSeconds: *cooldown,
	}

	log.Printf("Monitor starting: table=%s poll=%ds cooldown=%ds dry_run=%v terraform=%s",
		*tableName, *pollInterval, *cooldown, *dryRun, *terraformDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()

	ticker := time.NewTicker(time.Duration(*pollInterval) * time.Second)
	defer ticker.Stop()

	log.Println("Polling DynamoDB for new metrics...")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			metrics := pollDynamoDB(ctx, ddb, *tableName, state)
			if metrics == nil {
				continue
			}

			metric := *metrics
			log.Printf("Metric: cpu=%.1f%% p99=%.0fms rate=%.1f/s trend=%s instances=%d",
				metric.AvgCPUPercent, metric.P99LatencyMs, metric.RequestRatePerSec,
				metric.Trend, metric.InstanceCount)

			state.mu.Lock()
			state.history = append(state.history, metric)
			if len(state.history) > state.maxHistory {
				state.history = state.history[len(state.history)-state.maxHistory:]
			}
			history := make([]AggregatedMetric, len(state.history))
			copy(history, state.history)
			state.mu.Unlock()

			alerts := evaluateAlerts(metric, history)
			if len(alerts) == 0 {
				continue
			}

			state.mu.Lock()
			elapsed := time.Since(state.lastInvocation)
			withinCooldown := !state.lastInvocation.IsZero() && elapsed < time.Duration(state.cooldownSeconds)*time.Second
			state.mu.Unlock()

			if withinCooldown {
				log.Printf("Alert detected (%s) but within cooldown (%.0fs remaining)",
					alerts[0].Type, (time.Duration(state.cooldownSeconds)*time.Second - elapsed).Seconds())
				continue
			}

			prompt := buildClaudePrompt(alerts, metric, history, *terraformDir)

			if *dryRun {
				log.Println("=== DRY RUN: Would invoke Claude Code with prompt: ===")
				fmt.Println(prompt)
				log.Println("=== END DRY RUN ===")
			} else {
				log.Printf("Invoking Claude Code for alert: %s", alerts[0].Type)
				invokeClaudeCode(prompt)
			}

			state.mu.Lock()
			state.lastInvocation = time.Now()
			state.mu.Unlock()
		}
	}
}

// pollDynamoDB fetches the latest item from DynamoDB and returns it only if its
// timestamp is newer than the last one we processed (deduplication via lastTimestamp).
// This prevents re-triggering Claude Code on data we already acted on.
func pollDynamoDB(ctx context.Context, client *dynamodb.Client, tableName string, state *MonitorState) *AggregatedMetric {
	// Scan for latest items (sorted by timestamp descending)
	out, err := client.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(tableName),
		Limit:     aws.Int32(1),
	})
	if err != nil {
		log.Printf("DynamoDB scan error: %v", err)
		return nil
	}
	if len(out.Items) == 0 {
		return nil
	}

	// Parse the latest item
	var metric AggregatedMetric
	if err := attributevalue.UnmarshalMap(out.Items[0], &metric); err != nil {
		log.Printf("DynamoDB unmarshal error: %v", err)
		return nil
	}

	// Skip if we already saw this timestamp
	state.mu.Lock()
	if metric.Timestamp == state.lastTimestamp {
		state.mu.Unlock()
		return nil
	}
	state.lastTimestamp = metric.Timestamp
	state.mu.Unlock()

	return &metric
}

// evaluateAlerts applies scaling heuristics to the current metric snapshot and
// recent history. It checks five conditions:
//   1. HIGH_CPU_INCREASING  — CPU > 70% AND trend is "increasing" (proactive scale-up)
//   2. SUSTAINED_HIGH_CPU   — CPU > 80% for 3 consecutive 10s windows (reactive scale-up)
//   3. SUSTAINED_LOW_CPU    — CPU < 30% for 3 consecutive 10s windows (scale-down)
//   4. HIGH_ERROR_RATE      — 5xx error rate > 5%
//   5. HIGH_LATENCY         — p99 latency > 500ms
//
// Returns all triggered conditions. The caller invokes Claude Code only when at
// least one condition is triggered and the cooldown period has elapsed.
func evaluateAlerts(current AggregatedMetric, history []AggregatedMetric) []AlertCondition {
	var alerts []AlertCondition

	if current.AvgCPUPercent > 70 && current.Trend == "increasing" {
		alerts = append(alerts, AlertCondition{
			Type:    "HIGH_CPU_INCREASING",
			Message: fmt.Sprintf("CPU at %.1f%% with increasing trend", current.AvgCPUPercent),
		})
	}

	if len(history) >= 3 {
		highCount := 0
		for _, h := range history[len(history)-3:] {
			if h.AvgCPUPercent > 80 {
				highCount++
			}
		}
		if highCount == 3 {
			alerts = append(alerts, AlertCondition{
				Type:    "SUSTAINED_HIGH_CPU",
				Message: fmt.Sprintf("CPU above 80%% for 3+ consecutive windows (current: %.1f%%)", current.AvgCPUPercent),
			})
		}
	}

	if len(history) >= 3 {
		lowCount := 0
		for _, h := range history[len(history)-3:] {
			if h.AvgCPUPercent < 30 {
				lowCount++
			}
		}
		if lowCount == 3 {
			alerts = append(alerts, AlertCondition{
				Type:    "SUSTAINED_LOW_CPU",
				Message: fmt.Sprintf("CPU below 30%% for 3+ consecutive windows (current: %.1f%%)", current.AvgCPUPercent),
			})
		}
	}

	if current.ErrorRatePercent > 5 {
		alerts = append(alerts, AlertCondition{
			Type:    "HIGH_ERROR_RATE",
			Message: fmt.Sprintf("Error rate at %.1f%%", current.ErrorRatePercent),
		})
	}

	if current.P99LatencyMs > 500 {
		alerts = append(alerts, AlertCondition{
			Type:    "HIGH_LATENCY",
			Message: fmt.Sprintf("P99 latency at %.0fms", current.P99LatencyMs),
		})
	}

	return alerts
}

// buildClaudePrompt constructs a structured natural-language prompt that is passed
// to the Claude Code CLI. The prompt includes:
//   - Which alert conditions were triggered (and their current values)
//   - The latest AggregatedMetric snapshot
//   - The last N metric windows as JSON history for trend context
//   - Step-by-step instructions telling Claude how to query AWS, interpret the data,
//     and run terraform apply if scaling is warranted
//
// This is the "glue" between the metrics pipeline and the AI decision engine.
// Claude Code is given full AWS CLI and Terraform access to act autonomously.
func buildClaudePrompt(alerts []AlertCondition, current AggregatedMetric, history []AggregatedMetric, terraformDir string) string {
	alertLines := make([]string, len(alerts))
	for i, a := range alerts {
		alertLines[i] = fmt.Sprintf("  - [%s] %s", a.Type, a.Message)
	}

	historyJSON, _ := json.MarshalIndent(history, "  ", "  ")
	currentJSON, _ := json.MarshalIndent(current, "  ", "  ")

	return fmt.Sprintf(`INFRASTRUCTURE SCALING ALERT - Action Required

ALERTS TRIGGERED:
%s

CURRENT METRICS (latest 10-second window):
  %s

METRIC HISTORY (last %d windows):
  %s

INSTRUCTIONS:
You are an AI DevOps agent for the cs6650-final project on AWS ECS.

1. First, check the current ECS service status:
   aws ecs describe-services --cluster cs6650-final-cluster --services cs6650-final-cart-api --region us-west-2 --query "services[0].{Desired:desiredCount,Running:runningCount}" --no-cli-pager

2. Check recent CloudWatch logs for the cart-api:
   aws logs filter-log-events --log-group-name "/ecs/cs6650-final/cart-api" --start-time $(($(date +%%s) - 120))000 --limit 10 --region us-west-2 --query "events[].message" --output text --no-cli-pager

3. Check DynamoDB metrics table for recent trends:
   aws dynamodb scan --table-name cs6650-final-metrics --limit 5 --region us-west-2 --no-cli-pager --query "Items[:3]"

4. Based on all the data, decide if scaling is needed:
   - Scale UP if CPU is high (>70%%) with increasing trend or sustained high load
   - Scale DOWN if CPU is consistently low (<30%%) for multiple windows
   - No action if metrics are within normal range

5. If scaling is needed, modify the terraform config:
   - Edit %s/terraform.tfvars: change "ai_desired_count" to the new value
   - Change "scaling_mode" to "ai"
   - Run: cd %s && terraform apply -auto-approve -input=false
   - Constraints: min 1, max 8 tasks. Change by at most +-2 per decision.

6. After any action, report what you did and why.

IMPORTANT: Be conservative. Only scale if the data clearly supports it. Explain your reasoning.`,
		strings.Join(alertLines, "\n"),
		string(currentJSON),
		len(history),
		string(historyJSON),
		terraformDir,
		terraformDir,
	)
}

// invokeClaudeCode shells out to the `claude` CLI with the constructed prompt.
// --print:                       run non-interactively, print output then exit
// --dangerously-skip-permissions: allow Claude Code to execute shell commands
//                                 (aws, terraform) without per-command prompts
//
// Claude Code receives the full prompt as its task and autonomously:
//  1. Queries AWS ECS and CloudWatch to verify the alert
//  2. Decides whether to scale up, scale down, or take no action
//  3. Edits terraform.tfvars and runs terraform apply if scaling is needed
//  4. Reports its decision in plain text to stdout
func invokeClaudeCode(prompt string) {
	cmd := exec.Command("claude",
		"--print",
		"--dangerously-skip-permissions",
		prompt,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Println(">>> Claude Code invoked >>>")
	start := time.Now()

	if err := cmd.Run(); err != nil {
		log.Printf("Claude Code error: %v", err)
		return
	}

	log.Printf("<<< Claude Code finished (%.1fs) <<<", time.Since(start).Seconds())
}

func findTerraformDir() string {
	dir, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		candidate := dir + "/terraform"
		if _, err := os.Stat(candidate + "/main.tf"); err == nil {
			return candidate
		}
		dir = dir + "/.."
	}
	return "./terraform"
}
