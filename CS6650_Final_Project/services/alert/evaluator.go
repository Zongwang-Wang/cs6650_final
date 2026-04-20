package main

import (
	"fmt"
	"sync"
	"time"
)

const (
	windowDuration  = 30 * time.Second
	cooldownPeriod  = 5 * time.Minute

	// Thresholds adapted for album-store (no CPU — use latency + error + cache)
	latencyP99Ms    = int64(500)  // ms — alert if p99 > 500ms
	errorThreshold  = 5.0        // % — alert if 5xx > 5%
	cacheHitMin     = 50.0       // % — alert if cache hit rate < 50% for 30s
)

type instanceWindow struct {
	samples []MetricEvent
}

type Evaluator struct {
	mu        sync.Mutex
	windows   map[string]*instanceWindow
	cooldowns map[string]time.Time
	notifier  *Notifier
	metrics   *Metrics
}

func NewEvaluator(n *Notifier, m *Metrics) *Evaluator {
	return &Evaluator{
		windows:   make(map[string]*instanceWindow),
		cooldowns: make(map[string]time.Time),
		notifier:  n,
		metrics:   m,
	}
}

func (e *Evaluator) Evaluate(event MetricEvent) {
	// Skip non-application endpoints
	if event.Endpoint == "GET /health" || event.Endpoint == "GET /metrics" {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	iw, ok := e.windows[event.InstanceID]
	if !ok {
		iw = &instanceWindow{}
		e.windows[event.InstanceID] = iw
	}
	iw.samples = append(iw.samples, event)

	// Prune old samples
	cutoff := time.Now().Add(-windowDuration)
	trimmed := iw.samples[:0]
	for _, s := range iw.samples {
		t, err := time.Parse(time.RFC3339, s.Timestamp)
		if err != nil {
			continue
		}
		if !t.Before(cutoff) {
			trimmed = append(trimmed, s)
		}
	}
	iw.samples = trimmed

	if len(iw.samples) < 5 {
		return // need minimum samples before alerting
	}

	e.checkLatency(event.InstanceID, iw)
	e.checkErrorRate(event.InstanceID, iw)
	e.checkCacheHitRate(event.InstanceID, iw)
}

func (e *Evaluator) checkLatency(instanceID string, iw *instanceWindow) {
	latencies := make([]int64, len(iw.samples))
	for i, s := range iw.samples {
		latencies[i] = s.LatencyMs
	}
	sortInt64s(latencies)
	idx := int(float64(len(latencies))*0.99)
	if idx >= len(latencies) {
		idx = len(latencies) - 1
	}
	p99 := latencies[idx]

	if p99 > latencyP99Ms {
		e.fireAlert(instanceID, "HIGH_LATENCY", float64(p99), float64(latencyP99Ms),
			"p99 latency %dms exceeds %dms threshold over 30s window", p99, latencyP99Ms)
	} else {
		e.metrics.ActiveAlerts.WithLabelValues("HIGH_LATENCY").Set(0)
	}
}

func (e *Evaluator) checkErrorRate(instanceID string, iw *instanceWindow) {
	var total, errors int
	for _, s := range iw.samples {
		total++
		if s.StatusCode >= 500 {
			errors++
		}
	}
	if total == 0 {
		return
	}
	rate := float64(errors) / float64(total) * 100.0
	if rate > errorThreshold {
		e.fireAlert(instanceID, "HIGH_ERROR_RATE", rate, errorThreshold,
			"5xx error rate %.1f%% exceeds %.0f%% threshold", rate, errorThreshold)
	} else {
		e.metrics.ActiveAlerts.WithLabelValues("HIGH_ERROR_RATE").Set(0)
	}
}

func (e *Evaluator) checkCacheHitRate(instanceID string, iw *instanceWindow) {
	var gets, hits int
	for _, s := range iw.samples {
		if s.Method == "GET" {
			gets++
			if s.CacheHit {
				hits++
			}
		}
	}
	if gets < 5 {
		return // not enough GET samples
	}
	hitRate := float64(hits) / float64(gets) * 100.0
	if hitRate < cacheHitMin {
		e.fireAlert(instanceID, "LOW_CACHE_HIT", hitRate, cacheHitMin,
			"Redis cache hit rate %.1f%% below %.0f%% — possible Redis issue or cold cache", hitRate, cacheHitMin)
	} else {
		e.metrics.ActiveAlerts.WithLabelValues("LOW_CACHE_HIT").Set(0)
	}
}

func (e *Evaluator) fireAlert(instanceID, alertType string, value, threshold float64, msgFmt string, msgArgs ...any) {
	key := instanceID + ":" + alertType
	if t, ok := e.cooldowns[key]; ok && time.Since(t) < cooldownPeriod {
		return
	}
	e.cooldowns[key] = time.Now()
	e.metrics.AlertsFired.WithLabelValues(alertType).Inc()
	e.metrics.ActiveAlerts.WithLabelValues(alertType).Set(1)
	e.notifier.Send(Alert{
		Type:      alertType,
		Instance:  instanceID,
		Value:     value,
		Threshold: threshold,
		Message:   fmt.Sprintf(msgFmt, msgArgs...),
	})
}

func sortInt64s(a []int64) {
	for i := 1; i < len(a); i++ {
		key := a[i]
		j := i - 1
		for j >= 0 && a[j] > key {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = key
	}
}
