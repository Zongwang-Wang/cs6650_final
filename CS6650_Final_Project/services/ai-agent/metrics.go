package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	MetricsMessagesConsumed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ai_agent_messages_consumed_total",
		Help: "Total aggregated metric windows consumed from Kafka.",
	})

	MetricsDecisionsMade = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_agent_decisions_total",
		Help: "Total scaling decisions made, by action.",
	}, []string{"action"})

	MetricsTerraformApplies = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_agent_terraform_applies_total",
		Help: "Total Terraform apply attempts, by outcome.",
	}, []string{"result"})

	MetricsCurrentNodeCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ai_agent_current_node_count",
		Help: "Current number of album store EC2 nodes as known by the agent.",
	})

	MetricsDecisionLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ai_agent_decision_latency_seconds",
		Help:    "Time from metric received to decision published (includes Claude API call).",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30},
	})

	MetricsLastDecisionTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ai_agent_last_decision_timestamp_seconds",
		Help: "Unix timestamp of the most recent scaling decision.",
	})
)
