package main

import (
	"context"
	"log"
	"math"
	"sort"
	"sync"
	"time"
)

// AggregatedMetric is the output published to analytics-output-topic.
type AggregatedMetric struct {
	Timestamp        string  `json:"timestamp" dynamodbav:"timestamp"`
	WindowSeconds    int     `json:"window_seconds" dynamodbav:"window_seconds"`
	AvgCPUPercent    float64 `json:"avg_cpu_percent" dynamodbav:"avg_cpu_percent"`
	MaxCPUPercent    float64 `json:"max_cpu_percent" dynamodbav:"max_cpu_percent"`
	AvgLatencyMs     float64 `json:"avg_latency_ms" dynamodbav:"avg_latency_ms"`
	P99LatencyMs     float64 `json:"p99_latency_ms" dynamodbav:"p99_latency_ms"`
	RequestRatePerS  float64 `json:"request_rate_per_sec" dynamodbav:"request_rate_per_sec"`
	ErrorRatePercent float64 `json:"error_rate_percent" dynamodbav:"error_rate_percent"`
	Trend            string  `json:"trend" dynamodbav:"trend"`
	InstanceCount    int     `json:"instance_count" dynamodbav:"instance_count"`
}

type timestampedEvent struct {
	receivedAt time.Time
	event      MetricEvent
}

type Aggregator struct {
	mu         sync.Mutex
	window     []timestampedEvent
	prevAvgCPU float64
	producer   *Producer
	metrics    *Metrics
	dynamo     *DynamoWriter
}

const windowDuration = 60 * time.Second

func NewAggregator(p *Producer, m *Metrics, dw *DynamoWriter) *Aggregator {
	return &Aggregator{
		producer: p,
		metrics:  m,
		dynamo:   dw,
	}
}

func (a *Aggregator) Add(event MetricEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.window = append(a.window, timestampedEvent{
		receivedAt: time.Now(),
		event:      event,
	})
}

// Start runs the periodic aggregation loop every 10 seconds.
func (a *Aggregator) Start(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.computeAndPublish()
		}
	}
}

func (a *Aggregator) computeAndPublish() {
	a.mu.Lock()
	now := time.Now()
	cutoff := now.Add(-windowDuration)

	// Prune events outside the window.
	pruned := a.window[:0]
	for _, te := range a.window {
		if te.receivedAt.After(cutoff) {
			pruned = append(pruned, te)
		}
	}
	a.window = pruned

	// Copy current window for computation.
	events := make([]timestampedEvent, len(a.window))
	copy(events, a.window)
	a.mu.Unlock()

	a.metrics.WindowSize.Set(float64(len(events)))

	if len(events) == 0 {
		return
	}

	// Compute aggregates.
	var sumCPU, maxCPU float64
	var sumLatency float64
	latencies := make([]float64, 0, len(events))
	instances := make(map[string]struct{})
	var errorCount int

	for _, te := range events {
		e := te.event
		sumCPU += e.CPUPercent
		if e.CPUPercent > maxCPU {
			maxCPU = e.CPUPercent
		}
		sumLatency += float64(e.LatencyMs)
		latencies = append(latencies, float64(e.LatencyMs))
		instances[e.InstanceID] = struct{}{}
		if e.StatusCode >= 400 {
			errorCount++
		}
	}

	n := float64(len(events))
	avgCPU := sumCPU / n
	avgLatency := sumLatency / n
	errorRate := float64(errorCount) / n * 100.0
	requestRate := n / windowDuration.Seconds()

	// p99 latency.
	sort.Float64s(latencies)
	p99Idx := int(math.Ceil(0.99*float64(len(latencies)))) - 1
	if p99Idx < 0 {
		p99Idx = 0
	}
	p99Latency := latencies[p99Idx]

	// Trend detection: compare to previous window avg CPU.
	trend := "stable"
	if a.prevAvgCPU > 0 {
		diff := avgCPU - a.prevAvgCPU
		if diff > 5.0 {
			trend = "increasing"
		} else if diff < -5.0 {
			trend = "decreasing"
		}
	}
	a.prevAvgCPU = avgCPU

	agg := AggregatedMetric{
		Timestamp:        now.UTC().Format(time.RFC3339),
		WindowSeconds:    int(windowDuration.Seconds()),
		AvgCPUPercent:    math.Round(avgCPU*100) / 100,
		MaxCPUPercent:    math.Round(maxCPU*100) / 100,
		AvgLatencyMs:     math.Round(avgLatency*100) / 100,
		P99LatencyMs:     math.Round(p99Latency*100) / 100,
		RequestRatePerS:  math.Round(requestRate*100) / 100,
		ErrorRatePercent: math.Round(errorRate*100) / 100,
		Trend:            trend,
		InstanceCount:    len(instances),
	}

	a.metrics.AggregationTotal.Inc()
	a.metrics.AvgCPU.Set(agg.AvgCPUPercent)
	a.metrics.RequestRate.Set(agg.RequestRatePerS)

	a.producer.Publish(agg)
	if a.dynamo != nil {
		a.dynamo.PutMetric(agg)
	}
	log.Printf("aggregated: avg_cpu=%.1f%% p99_lat=%.0fms rate=%.1f/s trend=%s instances=%d",
		agg.AvgCPUPercent, agg.P99LatencyMs, agg.RequestRatePerS, agg.Trend, agg.InstanceCount)
}
