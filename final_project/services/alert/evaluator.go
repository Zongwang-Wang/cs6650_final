package main

import (
	"fmt"
	"sync"
	"time"
)

// Alert thresholds and timing constants.
// These match the values documented in IMPLEMENTATION_PLAN.md and are intentionally
// more aggressive than the analytics service's AI-agent thresholds (70% CPU) because
// the alert service is a safety net — it fires when the system is already in trouble,
// not just trending toward it.
const (
	windowDuration      = 30 * time.Second // rolling window for per-instance evaluation
	cooldownPeriod      = 5 * time.Minute  // minimum time between alerts of the same type per instance
	cpuThreshold        = 80.0             // percent — alert if ALL samples in 30s window exceed this
	errorThreshold      = 5.0              // percent — alert if 5xx error rate exceeds this
	latencyP99Threshold = 500              // ms — alert if p99 latency exceeds this
)

// instanceWindow holds recent metric samples for a single ECS task instance.
// Each cart-api task gets its own window, so a single overloaded instance
// triggers alerts independently without masking healthy instances.
type instanceWindow struct {
	samples []MetricEvent
}

// Evaluator is the rule engine of the alert service.
// It maintains per-instance sliding windows and fires alerts when thresholds
// are breached, with a cooldown to prevent notification storms.
//
// Design: the Evaluator consumes raw per-request MetricEvents directly from
// metrics-topic (the same topic as analytics). This gives it the lowest possible
// latency — it can fire within seconds of a threshold breach, whereas the
// analytics service aggregates over 60s before publishing.
type Evaluator struct {
	mu        sync.Mutex
	windows   map[string]*instanceWindow // keyed by instance_id (one per ECS task)
	cooldowns map[string]time.Time       // keyed by "instance_id:alert_type"
	notifier  *Notifier
	metrics   *Metrics
}

func NewEvaluator(notifier *Notifier, metrics *Metrics) *Evaluator {
	return &Evaluator{
		windows:   make(map[string]*instanceWindow),
		cooldowns: make(map[string]time.Time),
		notifier:  notifier,
		metrics:   metrics,
	}
}

// Evaluate is called once per MetricEvent received from Kafka.
// It updates the per-instance window and then runs all three threshold checks.
// The entire method runs under a single mutex to keep window state consistent.
func (e *Evaluator) Evaluate(event MetricEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	iw, ok := e.windows[event.InstanceID]
	if !ok {
		iw = &instanceWindow{}
		e.windows[event.InstanceID] = iw
	}

	iw.samples = append(iw.samples, event)

	// Trim samples outside the window.
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

	if len(iw.samples) == 0 {
		return
	}

	e.checkCPU(event.InstanceID, iw)
	e.checkErrorRate(event.InstanceID, iw)
	e.checkLatency(event.InstanceID, iw)
}

// checkCPU fires HIGH_CPU when CPU has been continuously above threshold for
// the full 30-second window. Requiring ALL samples (not just one) to exceed
// the threshold prevents transient spikes from generating false positives.
// The window span check ensures we have at least 30 seconds of evidence
// before alerting, not just a burst of events in the last few seconds.
func (e *Evaluator) checkCPU(instanceID string, iw *instanceWindow) {
	if len(iw.samples) < 2 {
		return
	}

	for _, s := range iw.samples {
		if s.CPUPercent <= cpuThreshold {
			e.metrics.ActiveAlerts.WithLabelValues("HIGH_CPU").Set(0)
			return
		}
	}

	earliest, _ := time.Parse(time.RFC3339, iw.samples[0].Timestamp)
	latest, _ := time.Parse(time.RFC3339, iw.samples[len(iw.samples)-1].Timestamp)
	if latest.Sub(earliest) < windowDuration {
		return
	}

	last := iw.samples[len(iw.samples)-1]
	e.fireAlert(instanceID, "HIGH_CPU", last.CPUPercent, cpuThreshold,
		"CPU usage at %.1f%% for 30+ seconds", last.CPUPercent)
}

// checkErrorRate fires HIGH_ERROR_RATE if error percentage exceeds threshold.
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
			"Error rate at %.1f%% over 30s window", rate)
	} else {
		e.metrics.ActiveAlerts.WithLabelValues("HIGH_ERROR_RATE").Set(0)
	}
}

// checkLatency fires HIGH_LATENCY if p99 latency exceeds threshold.
func (e *Evaluator) checkLatency(instanceID string, iw *instanceWindow) {
	if len(iw.samples) == 0 {
		return
	}

	// Collect latencies and compute p99 via simple sort.
	latencies := make([]int64, len(iw.samples))
	for i, s := range iw.samples {
		latencies[i] = s.LatencyMs
	}
	sortInt64s(latencies)

	idx := int(float64(len(latencies)) * 0.99)
	if idx >= len(latencies) {
		idx = len(latencies) - 1
	}
	p99 := latencies[idx]

	if p99 > latencyP99Threshold {
		e.fireAlert(instanceID, "HIGH_LATENCY", float64(p99), float64(latencyP99Threshold),
			"Latency p99 at %dms over 30s window", p99)
	} else {
		e.metrics.ActiveAlerts.WithLabelValues("HIGH_LATENCY").Set(0)
	}
}

// fireAlert is the single exit point for all alert conditions.
// It enforces the per-instance, per-type cooldown before delegating to the Notifier.
// The cooldown key is "instanceID:alertType" so a HIGH_CPU on instance-A does not
// suppress a HIGH_CPU on instance-B, and a HIGH_CPU does not suppress a HIGH_LATENCY
// on the same instance.
func (e *Evaluator) fireAlert(instanceID, alertType string, value, threshold float64, msgFmt string, msgArgs ...any) {
	key := instanceID + ":" + alertType
	if t, ok := e.cooldowns[key]; ok && time.Since(t) < cooldownPeriod {
		return // still within cooldown, suppress duplicate notification
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

// sortInt64s sorts a slice of int64 in ascending order (insertion sort, fine for small N).
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
