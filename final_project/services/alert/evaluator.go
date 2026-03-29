package main

import (
	"fmt"
	"sync"
	"time"
)

const (
	windowDuration  = 30 * time.Second
	cooldownPeriod  = 5 * time.Minute
	cpuThreshold    = 80.0
	errorThreshold  = 5.0   // percent
	latencyP99Threshold = 500 // ms
)

// instanceWindow holds recent metric samples for a single instance.
type instanceWindow struct {
	samples []MetricEvent
}

// Evaluator checks incoming metrics against alert thresholds.
type Evaluator struct {
	mu        sync.Mutex
	windows   map[string]*instanceWindow // keyed by instance_id
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

// Evaluate processes a single MetricEvent and checks all alert conditions.
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

// checkCPU fires HIGH_CPU if all samples in the window exceed the threshold
// and the window spans at least 30 seconds.
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
