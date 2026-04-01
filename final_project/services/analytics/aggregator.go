package main

import (
	"context"
	"log"
	"math"
	"sort"
	"sync"
	"time"
)

// AggregatedMetric is the output published to analytics-output-topic every 10 seconds.
// It summarizes the health of the entire cart-api fleet over the last 60 seconds.
// This struct is consumed by two downstream services:
//   - ai-agent: uses it to make ECS scaling decisions via Claude API + Terraform
//   - monitor:  uses it (via DynamoDB) to invoke Claude Code for manual scaling
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

// windowDuration is the rolling window size for aggregation.
// All MetricEvents older than 60 seconds are pruned on every computation cycle.
const windowDuration = 60 * time.Second

func NewAggregator(p *Producer, m *Metrics, dw *DynamoWriter) *Aggregator {
	return &Aggregator{
		producer: p,
		metrics:  m,
		dynamo:   dw,
	}
}

// Add is called by the Consumer goroutine for every MetricEvent received from Kafka.
// It appends the event to the in-memory sliding window under a mutex lock.
// The actual aggregation does NOT happen here — it is deferred to the ticker in Start().
func (a *Aggregator) Add(event MetricEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.window = append(a.window, timestampedEvent{
		receivedAt: time.Now(),
		event:      event,
	})
}

// Start runs the periodic aggregation loop every 10 seconds.
// This is the "clock" of the analytics pipeline: regardless of traffic volume,
// a summary is published every 10 seconds so downstream consumers always have
// a fresh view of the system even during quiet periods.
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

// computeAndPublish is the heart of the analytics service.
// It runs every 10 seconds and performs four steps:
//  1. Prune:   drop MetricEvents older than 60 seconds from the sliding window
//  2. Compute: calculate CPU, latency, error rate, and trend from remaining events
//  3. Publish: send the AggregatedMetric to analytics-output-topic for downstream consumers
//  4. Persist: optionally write to DynamoDB for the monitor service to read
func (a *Aggregator) computeAndPublish() {
	a.mu.Lock()
	now := time.Now()
	cutoff := now.Add(-windowDuration)

	// Step 1: Prune — evict events that have fallen outside the 60s window.
	// Using in-place slice reuse to avoid allocations on the hot path.
	pruned := a.window[:0]
	for _, te := range a.window {
		if te.receivedAt.After(cutoff) {
			pruned = append(pruned, te)
		}
	}
	a.window = pruned

	// Copy the window snapshot before releasing the lock so the long computation
	// below does not block the Consumer goroutine from calling Add().
	events := make([]timestampedEvent, len(a.window))
	copy(events, a.window)
	a.mu.Unlock()

	a.metrics.WindowSize.Set(float64(len(events)))

	if len(events) == 0 {
		return
	}

	// Step 2: Compute aggregates across all events in the snapshot.
	var maxCPU float64
	var sumLatency float64
	latencies := make([]float64, 0, len(events))
	instances := make(map[string]struct{}) // unique instance_id set → InstanceCount
	var errorCount int

	// Time-weighted CPU average: bucket events into 5s sampling windows
	// (matching cpuTracker's 5s sample interval), take one reading per bucket.
	// This mirrors CloudWatch's time-weighted approach and avoids high-traffic
	// periods inflating the average when request rate spikes alongside CPU.
	cpuBuckets := make(map[int64]float64) // unix_sec/5 → last cpu% in that bucket

	for _, te := range events {
		e := te.event
		bucket := te.receivedAt.Unix() / 5
		cpuBuckets[bucket] = e.CPUPercent
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

	// CPU average is computed over time buckets, not over requests.
	var sumCPU float64
	for _, cpu := range cpuBuckets {
		sumCPU += cpu
	}
	avgCPU := sumCPU / float64(len(cpuBuckets))

	n := float64(len(events))
	avgLatency := sumLatency / n
	errorRate := float64(errorCount) / n * 100.0
	// requestRate is events per second across the full 60s window.
	requestRate := n / windowDuration.Seconds()

	// p99 latency via sort: index = ceil(0.99 * n) - 1.
	// This is an exact p99, not an approximation, because we hold all raw values.
	sort.Float64s(latencies)
	p99Idx := int(math.Ceil(0.99*float64(len(latencies)))) - 1
	if p99Idx < 0 {
		p99Idx = 0
	}
	p99Latency := latencies[p99Idx]

	// Trend detection: compare current avgCPU to the previous cycle's avgCPU.
	// A delta > +5% signals load is growing; < -5% signals it is declining.
	// The ai-agent uses "trend=increasing" as a signal to scale up proactively
	// before CPU hits the hard threshold.
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

	// Update Prometheus gauges so Grafana can display real-time CPU and request rate.
	a.metrics.AggregationTotal.Inc()
	a.metrics.AvgCPU.Set(agg.AvgCPUPercent)
	a.metrics.RequestRate.Set(agg.RequestRatePerS)

	// Step 3: Publish the aggregation to analytics-output-topic.
	// The ai-agent consumes this topic to make Terraform scaling decisions.
	a.producer.Publish(agg)

	// Step 4: Persist to DynamoDB (only when DYNAMODB_TABLE env var is set).
	// The monitor service polls DynamoDB to feed data into Claude Code for
	// manual/interactive infrastructure management.
	if a.dynamo != nil {
		a.dynamo.PutMetric(agg)
	}
	log.Printf("aggregated: avg_cpu=%.1f%% p99_lat=%.0fms rate=%.1f/s trend=%s instances=%d",
		agg.AvgCPUPercent, agg.P99LatencyMs, agg.RequestRatePerS, agg.Trend, agg.InstanceCount)
}
