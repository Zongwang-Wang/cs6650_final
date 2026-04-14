// monitor — Album Store infrastructure monitor that invokes Claude Code as the AI DevOps agent.
//
// Mirrors the pattern from cs6650_final/final_project/services/monitor, adapted for:
//   - Prometheus (not DynamoDB) as the metric source
//   - Album store metrics: latency, cache hit rate, error rate (no CPU tracking)
//   - Optimization suggestions (not just scaling) as the primary Claude task
//   - Node count recommendation instead of ECS task count
//
// When conditions are detected, this program shells out to:
//   claude --print --dangerously-skip-permissions <prompt>
//
// Claude Code then runs AWS CLI / Terraform commands autonomously and reports its findings.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MetricSnapshot holds the latest values queried from Prometheus.
type MetricSnapshot struct {
	Timestamp        string
	RequestRatePerS  float64
	AvgLatencyMs     float64
	P99LatencyMs     float64
	ErrorRatePct     float64
	CacheHitRatePct  float64
	ActiveNodes      int
	RedisMemoryBytes float64
	DBConnections    float64
	S3Objects        float64
	Trend            string // from analytics service: "increasing"|"decreasing"|"stable"
	WindowSize       float64
}

// AlertCondition describes a triggered threshold.
type AlertCondition struct {
	Type    string
	Message string
}

type MonitorState struct {
	mu             sync.Mutex
	history        []MetricSnapshot
	maxHistory     int
	lastInvocation time.Time
	cooldown       time.Duration
}

// promQuery queries a single scalar from Prometheus.
func promQuery(baseURL, query string) (float64, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", baseURL, url.QueryEscape(query))
	resp, err := http.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Data struct {
			Result []struct {
				Value [2]interface{} `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Data.Result) == 0 {
		return 0, nil
	}

	valStr, ok := result.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, nil
	}
	return strconv.ParseFloat(valStr, 64)
}

func gatherMetrics(prometheusURL string) MetricSnapshot {
	q := func(query string) float64 {
		v, _ := promQuery(prometheusURL, query)
		return v
	}

	nodes := int(q("count(up{job='album_store'}==1)"))
	if nodes == 0 {
		nodes = 4 // default if prometheus not yet scraped
	}

	return MetricSnapshot{
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		RequestRatePerS:  q("rate(aws_applicationelb_request_count_sum[5m])"),
		AvgLatencyMs:     q("analytics_avg_latency_ms"),
		P99LatencyMs:     q("aws_applicationelb_target_response_time_p99 * 1000"),
		ErrorRatePct:     q("100 * rate(aws_applicationelb_httpcode_target_5_xx_count_sum[5m]) / (rate(aws_applicationelb_request_count_sum[5m]) + 0.001)"),
		CacheHitRatePct:  q("100 * sum(rate(album_store_cache_hits_total[5m])) / (sum(rate(album_store_cache_hits_total[5m])) + sum(rate(album_store_cache_misses_total[5m])) + 0.001)"),
		ActiveNodes:      nodes,
		RedisMemoryBytes: q("redis_memory_used_bytes"),
		DBConnections:    q("pg_stat_database_numbackends{datname='albumstore'}"),
		S3Objects:        q("s3_bucket_objects_total"),
		Trend:            func() string { v := q("analytics_cache_hit_rate_pct"); _ = v; return "stable" }(), // placeholder
		WindowSize:       q("analytics_window_size"),
	}
}

// evaluateConditions checks album-store-specific thresholds.
// Focus: latency, errors, cache health, capacity pressure — not CPU.
func evaluateConditions(current MetricSnapshot, history []MetricSnapshot) []AlertCondition {
	var alerts []AlertCondition

	if current.P99LatencyMs > 2000 {
		alerts = append(alerts, AlertCondition{
			Type:    "HIGH_LATENCY",
			Message: fmt.Sprintf("p99 latency %.0fms exceeds 2000ms — possible S3 bandwidth saturation or insufficient nodes", current.P99LatencyMs),
		})
	}

	if current.ErrorRatePct > 3 {
		alerts = append(alerts, AlertCondition{
			Type:    "HIGH_ERROR_RATE",
			Message: fmt.Sprintf("5xx error rate %.1f%% — investigate DB connections or S3 errors", current.ErrorRatePct),
		})
	}

	if current.CacheHitRatePct > 0 && current.CacheHitRatePct < 50 {
		alerts = append(alerts, AlertCondition{
			Type:    "LOW_CACHE_HIT",
			Message: fmt.Sprintf("Redis cache hit rate %.1f%% — warm-up needed or ElastiCache undersized", current.CacheHitRatePct),
		})
	}

	if current.RequestRatePerS > 200 && len(history) >= 3 {
		alerts = append(alerts, AlertCondition{
			Type:    "HIGH_LOAD",
			Message: fmt.Sprintf("%.0f req/s sustained — consider adding a node (currently %d/%d)", current.RequestRatePerS, current.ActiveNodes, 4),
		})
	}

	// Periodic review — even with no alerts, ask Claude for optimization suggestions every 30 min
	return alerts
}

// buildPrompt constructs the Claude Code prompt.
// Focus is on OPTIMIZATION SUGGESTIONS, not just binary scale up/down.
func buildPrompt(alerts []AlertCondition, current MetricSnapshot, history []MetricSnapshot, terraformDir string) string {
	alertSection := "No threshold violations detected — requesting routine optimization review."
	if len(alerts) > 0 {
		lines := make([]string, len(alerts))
		for i, a := range alerts {
			lines[i] = fmt.Sprintf("  [%s] %s", a.Type, a.Message)
		}
		alertSection = "ALERTS:\n" + strings.Join(lines, "\n")
	}

	curJSON, _ := json.MarshalIndent(current, "  ", "  ")
	histJSON, _ := json.MarshalIndent(history, "  ", "  ")

	return fmt.Sprintf(`ALBUM STORE INFRASTRUCTURE OPTIMIZATION REVIEW
================================================
%s

CURRENT SYSTEM STATE:
%s

METRIC HISTORY (last %d windows):
%s

INFRASTRUCTURE CONTEXT:
  - 4 EC2 nodes (c5n.large, 25 Gbps each) behind an AWS ALB
  - ElastiCache Redis (cache.t3.micro) — seq counters + album/photo status cache
  - RDS PostgreSQL db.t3.medium — albums and photos tables
  - S3 bucket — photo file storage (public-read, same region as EC2)
  - Terraform config at: %s
  - Tiered semaphore: 64 goroutines for files <10MB, 8 for ≥10MB per node

YOUR ROLE — AI DevOps Optimization Advisor:
You are Claude Code acting as an infrastructure advisor for this album store.
Your job is NOT just to scale up or down — it is to provide holistic optimization
suggestions covering all dimensions: caching, database, network, S3, code, and cost.

ANALYSIS TASKS:
1. Check current ALB and node health:
   aws elbv2 describe-target-health --target-group-arn $(cd %s && terraform output -raw instance_ids 2>/dev/null | head -1) --region us-west-2 --no-cli-pager 2>/dev/null || echo "Use: aws elbv2 describe-target-groups --region us-west-2"

2. Check recent CloudWatch metrics for EC2 nodes:
   aws cloudwatch get-metric-statistics --namespace AWS/EC2 --metric-name CPUUtilization \
     --start-time $(date -u -d '10 minutes ago' +%%Y-%%m-%%dT%%H:%%M:%%SZ) \
     --end-time $(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ) \
     --period 60 --statistics Average --region us-west-2 --no-cli-pager

3. Analyze the metrics and provide specific optimization suggestions for each area:

   a) CACHING: Is Redis being used effectively? Cache hit rate? TTL tuning needed?
   b) DATABASE: Connection pool sizing? Index usage? Query patterns?
   c) S3 UPLOADS: Tiered semaphore settings optimal? Upload latency analysis?
   d) NETWORK: Is 4 × 25Gbps adequate? ALB idle timeout correct?
   e) NODE COUNT: Scale up/down recommendation with exact justification [1-4 nodes]
   f) COST: Any unused resources? Cheaper alternatives for any component?
   g) RELIABILITY: Single points of failure? Redis being non-critical is good, but any risks?

4. If node scaling is recommended:
   - Current: %d nodes
   - Max: 4, Min: 1, change ±1 per action
   - To apply: cd %s && terraform apply -var='node_count=<N>' -auto-approve

5. Provide a PRIORITY-ORDERED action list with estimated impact for each suggestion.

FORMAT YOUR RESPONSE AS:
  STATUS: [healthy/warning/critical]
  TOP FINDING: [one sentence summary]

  OPTIMIZATION SUGGESTIONS (ordered by impact):
  1. [HIGH/MED/LOW] <suggestion> — Expected impact: <description>
  2. ...

  NODE RECOMMENDATION: [scale_up to N / scale_down to N / no_action]
  REASONING: <why>

  IMMEDIATE ACTIONS TAKEN: <any terraform/aws commands you ran>`,
		alertSection,
		string(curJSON),
		len(history),
		string(histJSON),
		terraformDir,
		terraformDir,
		current.ActiveNodes,
		terraformDir,
	)
}

func invokeClaudeCode(prompt string) {
	cmd := exec.Command("claude",
		"--print",
		"--dangerously-skip-permissions",
		prompt,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Println(">>> Invoking Claude Code as DevOps advisor...")
	start := time.Now()
	if err := cmd.Run(); err != nil {
		log.Printf("Claude Code error: %v", err)
	}
	log.Printf("<<< Claude Code finished in %.1fs", time.Since(start).Seconds())
}

func main() {
	prometheusURL := flag.String("prometheus", "http://52.13.41.234:9090", "Prometheus URL")
	terraformDir  := flag.String("terraform-dir", "", "Path to final_mastery/terraform/")
	cooldown      := flag.Duration("cooldown", 10*time.Minute, "Min time between Claude invocations")
	pollInterval  := flag.Duration("poll", 30*time.Second, "How often to poll Prometheus")
	dryRun        := flag.Bool("dry-run", false, "Print prompt without invoking Claude Code")
	periodic      := flag.Duration("periodic", 30*time.Minute, "Periodic review even with no alerts (0 = disable)")
	flag.Parse()

	if *terraformDir == "" {
		// Default: find terraform relative to this binary
		if _, err := os.Stat("../../final_mastery/terraform/main.tf"); err == nil {
			*terraformDir = "../../final_mastery/terraform"
		} else {
			*terraformDir = "./terraform"
		}
	}

	// Check claude CLI is available
	if !*dryRun {
		if _, err := exec.LookPath("claude"); err != nil {
			log.Fatal("ERROR: 'claude' CLI not found in PATH. Run with --dry-run to test.")
		}
	}

	state := &MonitorState{
		maxHistory: 10,
		cooldown:   *cooldown,
	}

	log.Printf("Album Store DevOps Monitor starting")
	log.Printf("  Prometheus:  %s", *prometheusURL)
	log.Printf("  Terraform:   %s", *terraformDir)
	log.Printf("  Poll:        %s", *pollInterval)
	log.Printf("  Cooldown:    %s", *cooldown)
	log.Printf("  Periodic:    %s", *periodic)
	log.Printf("  Dry run:     %v", *dryRun)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(*pollInterval)
	defer ticker.Stop()

	var lastPeriodic time.Time

	for {
		select {
		case <-sigCh:
			log.Println("Shutting down monitor.")
			return

		case <-ticker.C:
			snap := gatherMetrics(*prometheusURL)

			state.mu.Lock()
			state.history = append(state.history, snap)
			if len(state.history) > state.maxHistory {
				state.history = state.history[len(state.history)-state.maxHistory:]
			}
			history := make([]MetricSnapshot, len(state.history))
			copy(history, state.history)
			lastInvoke := state.lastInvocation
			state.mu.Unlock()

			alerts := evaluateConditions(snap, history)

			// Check if periodic review is due
			periodicDue := *periodic > 0 && time.Since(lastPeriodic) >= *periodic

			if len(alerts) == 0 && !periodicDue {
				log.Printf("metrics: p99=%.0fms cache=%.0f%% err=%.1f%% nodes=%d req/s=%.0f — ok",
					snap.P99LatencyMs, snap.CacheHitRatePct, snap.ErrorRatePct,
					snap.ActiveNodes, snap.RequestRatePerS)
				continue
			}

			if len(alerts) > 0 {
				log.Printf("CONDITIONS: %d alert(s) detected", len(alerts))
			} else {
				log.Printf("PERIODIC REVIEW due (last: %v ago)", time.Since(lastPeriodic).Round(time.Minute))
			}

			// Cooldown check
			if !lastInvoke.IsZero() && time.Since(lastInvoke) < *cooldown {
				remaining := *cooldown - time.Since(lastInvoke)
				log.Printf("Within cooldown — %.0f minutes remaining", remaining.Minutes())
				continue
			}

			prompt := buildPrompt(alerts, snap, history, *terraformDir)

			if *dryRun {
				fmt.Println("=== DRY RUN — Claude Code prompt ===")
				fmt.Println(prompt)
				fmt.Println("=== END ===")
			} else {
				invokeClaudeCode(prompt)
			}

			state.mu.Lock()
			state.lastInvocation = time.Now()
			state.mu.Unlock()
			lastPeriodic = time.Now()
		}
	}
}
