package main

import (
	"context"
	"log"
	"math"
	"sort"
	"sync"
	"time"
)

// AggregatedMetric is published to album-analytics-output every 10 seconds.
// Adapted from the original cart-api analytics: CPU fields removed,
// CacheHitRate added to reflect album-store's Redis caching behaviour.
type AggregatedMetric struct {
	Timestamp        string  `json:"timestamp"`
	WindowSeconds    int     `json:"window_seconds"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	P99LatencyMs     float64 `json:"p99_latency_ms"`
	RequestRatePerS  float64 `json:"request_rate_per_sec"`
	ErrorRatePercent float64 `json:"error_rate_percent"`
	CacheHitRate     float64 `json:"cache_hit_rate"`  // 0–100 %
	Trend            string  `json:"trend"`           // latency trend vs prev window
	InstanceCount    int     `json:"instance_count"`
}

type timestampedEvent struct {
	receivedAt time.Time
	event      MetricEvent
}

type Aggregator struct {
	mu           sync.Mutex
	window       []timestampedEvent
	prevAvgLat   float64
	producer     *Producer
	metrics      *Metrics
}

const windowDuration = 60 * time.Second

func NewAggregator(p *Producer, m *Metrics) *Aggregator {
	return &Aggregator{producer: p, metrics: m}
}

func (a *Aggregator) Add(event MetricEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.window = append(a.window, timestampedEvent{receivedAt: time.Now(), event: event})
}

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

	pruned := a.window[:0]
	for _, te := range a.window {
		if te.receivedAt.After(cutoff) {
			pruned = append(pruned, te)
		}
	}
	a.window = pruned

	events := make([]timestampedEvent, len(a.window))
	copy(events, a.window)
	a.mu.Unlock()

	a.metrics.WindowSize.Set(float64(len(events)))

	if len(events) == 0 {
		return
	}

	var sumLatency float64
	var errorCount, cacheHits, cacheTotal int
	latencies := make([]float64, 0, len(events))
	instances := make(map[string]struct{})

	for _, te := range events {
		e := te.event
		// Skip health/metrics endpoints from latency stats to keep numbers meaningful
		if e.Endpoint == "GET /health" || e.Endpoint == "GET /metrics" {
			continue
		}
		sumLatency += float64(e.LatencyMs)
		latencies = append(latencies, float64(e.LatencyMs))
		instances[e.InstanceID] = struct{}{}
		if e.StatusCode >= 500 {
			errorCount++
		}
		// Only GET endpoints exercise the cache
		if e.Method == "GET" {
			cacheTotal++
			if e.CacheHit {
				cacheHits++
			}
		}
	}

	if len(latencies) == 0 {
		return
	}

	n := float64(len(latencies))
	avgLatency := sumLatency / n
	errorRate := float64(errorCount) / n * 100.0
	requestRate := float64(len(events)) / windowDuration.Seconds()

	sort.Float64s(latencies)
	p99Idx := int(math.Ceil(0.99*n)) - 1
	if p99Idx < 0 {
		p99Idx = 0
	}
	p99 := latencies[p99Idx]

	var cacheHitRate float64
	if cacheTotal > 0 {
		cacheHitRate = float64(cacheHits) / float64(cacheTotal) * 100.0
	}

	// Latency trend (mirrors original CPU trend logic)
	trend := "stable"
	if a.prevAvgLat > 0 {
		diff := avgLatency - a.prevAvgLat
		if diff > a.prevAvgLat*0.10 {
			trend = "increasing"
		} else if diff < -a.prevAvgLat*0.10 {
			trend = "decreasing"
		}
	}
	a.prevAvgLat = avgLatency

	agg := AggregatedMetric{
		Timestamp:        now.UTC().Format(time.RFC3339),
		WindowSeconds:    int(windowDuration.Seconds()),
		AvgLatencyMs:     math.Round(avgLatency*100) / 100,
		P99LatencyMs:     math.Round(p99*100) / 100,
		RequestRatePerS:  math.Round(requestRate*100) / 100,
		ErrorRatePercent: math.Round(errorRate*100) / 100,
		CacheHitRate:     math.Round(cacheHitRate*100) / 100,
		Trend:            trend,
		InstanceCount:    len(instances),
	}

	a.metrics.AggregationTotal.Inc()
	a.metrics.AvgLatency.Set(agg.AvgLatencyMs)
	a.metrics.RequestRate.Set(agg.RequestRatePerS)
	a.metrics.CacheHitRate.Set(agg.CacheHitRate)

	a.producer.Publish(agg)

	log.Printf("analytics: avg_lat=%.0fms p99=%.0fms rate=%.1f/s err=%.1f%% cache=%.0f%% trend=%s nodes=%d",
		agg.AvgLatencyMs, agg.P99LatencyMs, agg.RequestRatePerS,
		agg.ErrorRatePercent, agg.CacheHitRate, agg.Trend, agg.InstanceCount)
}
