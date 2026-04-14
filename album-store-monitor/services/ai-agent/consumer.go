package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

// AggregatedMetric matches what our analytics service publishes to album-analytics-output.
// Adapted from original: CPU fields replaced with CacheHitRate for album store.
type AggregatedMetric struct {
	Timestamp        string  `json:"timestamp"`
	WindowSeconds    int     `json:"window_seconds"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	P99LatencyMs     float64 `json:"p99_latency_ms"`
	RequestRatePerS  float64 `json:"request_rate_per_sec"`
	ErrorRatePercent float64 `json:"error_rate_percent"`
	CacheHitRate     float64 `json:"cache_hit_rate"`  // % Redis cache hits on GET ops
	Trend            string  `json:"trend"`           // "increasing" | "decreasing" | "stable"
	InstanceCount    int     `json:"instance_count"`  // unique EC2 nodes seen in window
}

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

	log.Printf("AI agent consumer started: topic=%s group=%s", cfg.KafkaConsumerTopic, cfg.ConsumerGroup)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("consumer read error: %v", err)
			continue
		}

		MetricsMessagesConsumed.Inc()

		var metric AggregatedMetric
		if err := json.Unmarshal(msg.Value, &metric); err != nil {
			log.Printf("unmarshal error: %v", err)
			continue
		}

		log.Printf("received metric: p99=%.0fms rate=%.1f/s err=%.1f%% cache=%.0f%% trend=%s nodes=%d",
			metric.P99LatencyMs, metric.RequestRatePerS, metric.ErrorRatePercent,
			metric.CacheHitRate, metric.Trend, metric.InstanceCount)

		processMetric(ctx, cfg, state, claude, producer, metric)
	}
}

func processMetric(ctx context.Context, cfg Config, state *AgentState, claude *ClaudeClient, producer *EventProducer, metric AggregatedMetric) {
	start := time.Now()

	state.mu.Lock()
	state.MetricHistory = append(state.MetricHistory, metric)
	if len(state.MetricHistory) > state.MaxHistoryWindows {
		state.MetricHistory = state.MetricHistory[len(state.MetricHistory)-state.MaxHistoryWindows:]
	}
	history := make([]AggregatedMetric, len(state.MetricHistory))
	copy(history, state.MetricHistory)
	currentNodes := state.CurrentNodeCount
	lastApply := state.LastApplyTime
	state.mu.Unlock()

	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
	inCooldown := !lastApply.IsZero() && time.Since(lastApply) < cooldown

	decision, err := claude.GetScalingDecision(ctx, metric, history, currentNodes)
	if err != nil {
		log.Printf("Claude error: %v", err)
		return
	}

	log.Printf("Claude: action=%s confidence=%.2f reason=%q", decision.Action, decision.Confidence, decision.Reasoning)
	MetricsDecisionsMade.WithLabelValues(decision.Action).Inc()

	event := ScalingDecision{
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
		Action:            decision.Action,
		Reasoning:         decision.Reasoning,
		PreviousNodeCount: currentNodes,
		NewNodeCount:      currentNodes,
		Confidence:        decision.Confidence,
		Applied:           false,
		DryRun:            cfg.DryRun,
	}

	if decision.Action != "no_action" && decision.Changes.NodeCount > 0 {
		newCount := decision.Changes.NodeCount
		// Clamp to [1, 4] and max ±1 per cycle
		if newCount < 1 { newCount = 1 }
		if newCount > 4 { newCount = 4 }
		diff := newCount - currentNodes
		if diff > 1  { newCount = currentNodes + 1 }
		if diff < -1 { newCount = currentNodes - 1 }

		event.NewNodeCount = newCount

		shouldApply := decision.Confidence >= 0.7 && newCount != currentNodes && !inCooldown

		if inCooldown && newCount != currentNodes {
			log.Printf("cooldown: %.0fs remaining", (cooldown - time.Since(lastApply)).Seconds())
		}

		if shouldApply {
			if cfg.DryRun {
				log.Printf("DRY RUN: would change nodes %d → %d", currentNodes, newCount)
			} else {
				if err := ApplyTerraform(cfg.TerraformDir, newCount); err != nil {
					log.Printf("terraform apply failed: %v", err)
					MetricsTerraformApplies.WithLabelValues("failure").Inc()
				} else {
					log.Printf("terraform applied: nodes %d → %d", currentNodes, newCount)
					MetricsTerraformApplies.WithLabelValues("success").Inc()
					event.Applied = true
					state.mu.Lock()
					state.CurrentNodeCount = newCount
					state.LastApplyTime = time.Now()
					state.mu.Unlock()
					MetricsCurrentNodeCount.Set(float64(newCount))
				}
			}
		}
	}

	state.mu.Lock()
	state.LastDecision = &event
	state.mu.Unlock()

	MetricsLastDecisionTimestamp.SetToCurrentTime()
	MetricsDecisionLatency.Observe(time.Since(start).Seconds())

	producer.PublishEvent(ctx, event)
}
