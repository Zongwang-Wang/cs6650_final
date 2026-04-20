package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// MetricsMessagesConsumed counts the total number of aggregated metric
	// messages consumed from Kafka.
	MetricsMessagesConsumed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ai_agent_messages_consumed_total",
		Help: "Total number of aggregated metric messages consumed from Kafka",
	})

	// MetricsDecisionsMade counts scaling decisions by action type.
	MetricsDecisionsMade = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_agent_decisions_total",
		Help: "Total scaling decisions made, labeled by action type",
	}, []string{"action"})

	// MetricsTerraformApplies counts terraform apply attempts by result.
	MetricsTerraformApplies = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_agent_terraform_applies_total",
		Help: "Total terraform apply attempts, labeled by result (success/failure)",
	}, []string{"result"})

	// MetricsCurrentTaskCount tracks the current desired ECS task count.
	MetricsCurrentTaskCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ai_agent_current_task_count",
		Help: "Current desired ECS task count set by the AI agent",
	})

	// MetricsDecisionLatency measures the time from receiving a metric to
	// completing the decision cycle (including terraform apply if applicable).
	MetricsDecisionLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ai_agent_decision_latency_seconds",
		Help:    "Time from metric received to decision cycle completed",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
	})

	// MetricsLastDecisionTimestamp records the timestamp of the last decision.
	MetricsLastDecisionTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ai_agent_last_decision_timestamp",
		Help: "Unix timestamp of the last scaling decision",
	})
)
