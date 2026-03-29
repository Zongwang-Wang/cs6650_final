package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

// AggregatedMetric represents the analytics output from the analytics service.
type AggregatedMetric struct {
	Timestamp         string  `json:"timestamp"`
	WindowSeconds     int     `json:"window_seconds"`
	AvgCPUPercent     float64 `json:"avg_cpu_percent"`
	MaxCPUPercent     float64 `json:"max_cpu_percent"`
	AvgLatencyMs      float64 `json:"avg_latency_ms"`
	P99LatencyMs      float64 `json:"p99_latency_ms"`
	RequestRatePerSec float64 `json:"request_rate_per_sec"`
	ErrorRatePercent  float64 `json:"error_rate_percent"`
	Trend             string  `json:"trend"`
	InstanceCount     int     `json:"instance_count"`
}

// StartConsumer runs the Kafka consumer loop, reading aggregated metrics and
// feeding them into the decision engine.
func StartConsumer(ctx context.Context, cfg Config, state *AgentState, claude *ClaudeClient, producer *EventProducer) {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.KafkaBrokers,
		Topic:          cfg.KafkaConsumerTopic,
		GroupID:        cfg.ConsumerGroup,
		MinBytes:       1,
		MaxBytes:       10e6,
		CommitInterval: time.Second,
		StartOffset:    kafka.LastOffset,
	})
	defer reader.Close()

	log.Printf("Consumer started: topic=%s group=%s", cfg.KafkaConsumerTopic, cfg.ConsumerGroup)

	for {
		select {
		case <-ctx.Done():
			log.Println("Consumer shutting down...")
			return
		default:
		}

		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Consumer read error: %v", err)
			continue
		}

		MetricsMessagesConsumed.Inc()

		var metric AggregatedMetric
		if err := json.Unmarshal(msg.Value, &metric); err != nil {
			log.Printf("Failed to deserialize metric: %v", err)
			continue
		}

		log.Printf("Received metric: timestamp=%s avg_cpu=%.1f%% p99_latency=%.1fms rate=%.1f/s trend=%s instances=%d",
			metric.Timestamp, metric.AvgCPUPercent, metric.P99LatencyMs,
			metric.RequestRatePerSec, metric.Trend, metric.InstanceCount)

		processMetric(ctx, cfg, state, claude, producer, metric)
	}
}

// processMetric handles a single aggregated metric: updates history, consults
// Claude, validates the decision, optionally applies via Terraform, and
// publishes the event.
func processMetric(ctx context.Context, cfg Config, state *AgentState, claude *ClaudeClient, producer *EventProducer, metric AggregatedMetric) {
	startTime := time.Now()

	// Update history buffer
	state.mu.Lock()
	state.MetricHistory = append(state.MetricHistory, metric)
	if len(state.MetricHistory) > state.MaxHistoryWindows {
		state.MetricHistory = state.MetricHistory[len(state.MetricHistory)-state.MaxHistoryWindows:]
	}
	history := make([]AggregatedMetric, len(state.MetricHistory))
	copy(history, state.MetricHistory)
	currentTaskCount := state.CurrentTaskCount
	lastApplyTime := state.LastApplyTime
	state.mu.Unlock()

	// Check cooldown
	cooldownDuration := time.Duration(cfg.CooldownSeconds) * time.Second
	withinCooldown := !lastApplyTime.IsZero() && time.Since(lastApplyTime) < cooldownDuration

	// Call Claude for scaling decision
	decision, err := claude.GetScalingDecision(ctx, metric, history, currentTaskCount)
	if err != nil {
		log.Printf("Claude API error: %v", err)
		return
	}

	log.Printf("Claude decision: action=%s confidence=%.2f reasoning=%q",
		decision.Action, decision.Confidence, decision.Reasoning)

	MetricsDecisionsMade.WithLabelValues(decision.Action).Inc()

	// Build the event record
	event := ScalingDecision{
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
		Action:            decision.Action,
		Reasoning:         decision.Reasoning,
		PreviousTaskCount: currentTaskCount,
		NewTaskCount:      currentTaskCount,
		Confidence:        decision.Confidence,
		Applied:           false,
		DryRun:            cfg.DryRun,
	}

	// Determine new task count from decision
	if decision.Action != "no_action" && decision.Changes.CartAPITaskCount > 0 {
		newCount := decision.Changes.CartAPITaskCount

		// Validate bounds [1, 8]
		if newCount < 1 {
			newCount = 1
		}
		if newCount > 8 {
			newCount = 8
		}

		// Validate max change per cycle: +/-2
		diff := newCount - currentTaskCount
		if diff > 2 {
			newCount = currentTaskCount + 2
			log.Printf("Clamped scale-up to +2: %d -> %d", currentTaskCount, newCount)
		}
		if diff < -2 {
			newCount = currentTaskCount - 2
			log.Printf("Clamped scale-down to -2: %d -> %d", currentTaskCount, newCount)
		}

		event.NewTaskCount = newCount

		// Check if we should apply
		shouldApply := decision.Confidence >= 0.7 && newCount != currentTaskCount && !withinCooldown

		if withinCooldown && newCount != currentTaskCount {
			log.Printf("Skipping apply: within cooldown period (%.0fs remaining)",
				(cooldownDuration - time.Since(lastApplyTime)).Seconds())
		}

		if shouldApply {
			if cfg.DryRun {
				log.Printf("DRY RUN: would change task count %d -> %d", currentTaskCount, newCount)
				event.Applied = false
			} else {
				err := ApplyTerraform(cfg.TerraformDir, newCount)
				if err != nil {
					log.Printf("Terraform apply failed: %v", err)
					MetricsTerraformApplies.WithLabelValues("failure").Inc()
				} else {
					log.Printf("Terraform applied: task count %d -> %d", currentTaskCount, newCount)
					MetricsTerraformApplies.WithLabelValues("success").Inc()
					event.Applied = true

					state.mu.Lock()
					state.CurrentTaskCount = newCount
					state.LastApplyTime = time.Now()
					state.mu.Unlock()

					MetricsCurrentTaskCount.Set(float64(newCount))
				}
			}
		}
	}

	// Record decision
	state.mu.Lock()
	state.LastDecision = &event
	state.mu.Unlock()

	MetricsLastDecisionTimestamp.SetToCurrentTime()

	elapsed := time.Since(startTime).Seconds()
	MetricsDecisionLatency.Observe(elapsed)

	// Publish event to Kafka regardless of apply outcome
	if err := producer.PublishEvent(ctx, event); err != nil {
		log.Printf("Failed to publish infra event: %v", err)
	}
}
