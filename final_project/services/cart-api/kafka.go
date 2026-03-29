package main

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"sync"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"
)

type MetricEvent struct {
	Timestamp  string  `json:"timestamp"`
	Service    string  `json:"service"`
	InstanceID string  `json:"instance_id"`
	Endpoint   string  `json:"endpoint"`
	Method     string  `json:"method"`
	StatusCode int     `json:"status_code"`
	LatencyMs  int64   `json:"latency_ms"`
	CPUPercent float64 `json:"cpu_percent"`
}

type KafkaProducer struct {
	writer     *kafka.Writer
	instanceID string
	metrics    *Metrics
}

func NewKafkaProducer(brokers []string, topic, instanceID string) *KafkaProducer {
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
		WriteTimeout: 5 * time.Second,
		RequiredAcks: kafka.RequireOne,
		Async:        false,
	}

	return &KafkaProducer{
		writer:     w,
		instanceID: instanceID,
	}
}

func (p *KafkaProducer) SetMetrics(m *Metrics) {
	p.metrics = m
}

func (p *KafkaProducer) PublishMetric(endpoint, method string, statusCode int, latency time.Duration) {
	event := MetricEvent{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Service:    "cart-api",
		InstanceID: p.instanceID,
		Endpoint:   endpoint,
		Method:     method,
		StatusCode: statusCode,
		LatencyMs:  latency.Milliseconds(),
		CPUPercent: estimateCPUPercent(),
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("failed to marshal metric event: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(p.instanceID),
		Value: data,
	})

	if p.metrics != nil {
		if err != nil {
			log.Printf("failed to publish metric to kafka: %v", err)
			p.metrics.KafkaPublishTotal.WithLabelValues("failure").Inc()
		} else {
			p.metrics.KafkaPublishTotal.WithLabelValues("success").Inc()
		}
	}
}

func (p *KafkaProducer) Close() {
	if err := p.writer.Close(); err != nil {
		log.Printf("failed to close kafka writer: %v", err)
	}
}

// cpuTracker measures actual CPU usage by sampling process CPU time.
// On ECS Fargate with 256 CPU units (0.25 vCPU), 100% means fully using
// the allocated CPU share.
var cpuTracker = newCPUTracker()

type cpuTrackerState struct {
	mu          sync.Mutex
	lastTime    time.Time
	lastCPU     time.Duration
	currentPct  float64
	allocatedCPU float64 // in cores (256 units = 0.25 cores)
}

func newCPUTracker() *cpuTrackerState {
	ct := &cpuTrackerState{
		lastTime:     time.Now(),
		allocatedCPU: 0.25, // 256 CPU units = 0.25 vCPU; reports % of allocated capacity
	}
	// Read initial CPU time
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err == nil {
		ct.lastCPU = tvToDuration(usage.Utime) + tvToDuration(usage.Stime)
	}
	// Sample every 5 seconds in background
	go ct.sampleLoop()
	return ct
}

func (ct *cpuTrackerState) sampleLoop() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		ct.sample()
	}
}

func (ct *cpuTrackerState) sample() {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return
	}

	now := time.Now()
	cpuTime := tvToDuration(usage.Utime) + tvToDuration(usage.Stime)

	ct.mu.Lock()
	defer ct.mu.Unlock()

	elapsed := now.Sub(ct.lastTime).Seconds()
	if elapsed <= 0 {
		return
	}

	cpuDelta := (cpuTime - ct.lastCPU).Seconds()
	// CPU usage as percentage of allocated CPU
	// cpuDelta/elapsed = cores used, divide by allocatedCPU for percentage
	ct.currentPct = (cpuDelta / elapsed / ct.allocatedCPU) * 100.0

	if ct.currentPct > 100.0 {
		ct.currentPct = 100.0
	}
	if ct.currentPct < 0 {
		ct.currentPct = 0
	}

	ct.lastTime = now
	ct.lastCPU = cpuTime
}

func (ct *cpuTrackerState) get() float64 {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return math.Round(ct.currentPct*100) / 100
}

func tvToDuration(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

// estimateCPUPercent returns actual CPU usage as percentage of allocated vCPU.
func estimateCPUPercent() float64 {
	return cpuTracker.get()
}
