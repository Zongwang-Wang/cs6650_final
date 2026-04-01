package main

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
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
	mu              sync.Mutex
	lastTime        time.Time
	lastCPU         time.Duration // fallback: Getrusage accumulated CPU time
	lastCgroupNanos int64         // cgroup: last reading in nanoseconds
	useCgroup       bool          // whether cgroup path is available
	useECSMetadata  bool          // whether ECS Task Metadata Endpoint is available (highest priority)
	currentPct      float64
	currentMemBytes uint64  // latest container memory usage in bytes (ECS metadata only)
	allocatedCPU    float64 // in cores (256 units = 0.25 cores)
}

func newCPUTracker() *cpuTrackerState {
	allocatedCPU := 0.25 // default: 256 CPU units = 0.25 vCPU
	if v := os.Getenv("ALLOCATED_CPU"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed > 0 {
			allocatedCPU = parsed
		}
	}
	ct := &cpuTrackerState{
		lastTime:     time.Now(),
		allocatedCPU: allocatedCPU,
	}
	// Source priority (highest accuracy first):
	//   1. ECS Task Metadata Endpoint v4 — container-level, includes throttling,
	//      same formula as CloudWatch. Only available inside ECS Fargate tasks.
	//   2. cgroup — container-level, no throttling data, available on Linux.
	//   3. Getrusage — Go-process-level only, macOS / local dev fallback.
	if stats, ok := ReadECSTaskStats(allocatedCPU); ok {
		ct.useECSMetadata = true
		ct.currentPct = stats.CPUPercent
		ct.currentMemBytes = stats.MemoryBytes
		log.Println("cpu tracker: using ECS Task Metadata Endpoint v4 (container-level + throttling)")
	} else if nanos, ok := readCgroupCPUNanos(); ok {
		ct.useCgroup = true
		ct.lastCgroupNanos = nanos
		log.Println("cpu tracker: using cgroup (container-level)")
	} else {
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err == nil {
			ct.lastCPU = tvToDuration(usage.Utime) + tvToDuration(usage.Stime)
		}
		log.Println("cpu tracker: using Getrusage (process-level fallback)")
	}
	go ct.sampleLoop()
	return ct
}

// readCgroupMemoryBytes returns the container's current memory usage in bytes
// from the cgroup filesystem. Tries cgroups v2 (memory.current) then v1
// (memory.usage_in_bytes). Returns 0 when neither path is available.
func readCgroupMemoryBytes() uint64 {
	// cgroups v2
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.current"); err == nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			return v
		}
	}
	// cgroups v1
	if data, err := os.ReadFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); err == nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			return v
		}
	}
	return 0
}

// readCgroupCPUNanos returns total container CPU usage in nanoseconds.
// Tries cgroups v2 (cpu.stat usage_usec) then v1 (cpuacct.usage).
func readCgroupCPUNanos() (int64, bool) {
	// cgroups v2
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "usage_usec ") {
				fields := strings.Fields(line)
				if len(fields) == 2 {
					if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						return v * 1000, true // microseconds → nanoseconds
					}
				}
			}
		}
	}
	// cgroups v1
	if data, err := os.ReadFile("/sys/fs/cgroup/cpuacct/cpuacct.usage"); err == nil {
		v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err == nil {
			return v, true // already nanoseconds
		}
	}
	return 0, false
}

func (ct *cpuTrackerState) sampleLoop() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		ct.sample()
	}
}

func (ct *cpuTrackerState) sample() {
	now := time.Now()

	ct.mu.Lock()
	defer ct.mu.Unlock()

	elapsed := now.Sub(ct.lastTime).Seconds()
	if elapsed <= 0 {
		return
	}

	if ct.useECSMetadata {
		// ECS Task Metadata already provides a pre-computed 1-second delta
		// (cpu_stats vs precpu_stats), so we just call it and store the result.
		stats, ok := ReadECSTaskStats(ct.allocatedCPU)
		if !ok {
			return
		}
		ct.currentPct = stats.CPUPercent
		ct.currentMemBytes = stats.MemoryBytes
		ct.lastTime = now
		return
	}

	var cpuDeltaSec float64

	if ct.useCgroup {
		nanos, ok := readCgroupCPUNanos()
		if !ok {
			return
		}
		cpuDeltaSec = float64(nanos-ct.lastCgroupNanos) / 1e9
		ct.lastCgroupNanos = nanos
		// Read memory from cgroup on the same sample tick so both metrics
		// are time-consistent. ECS mode already populates this in the branch above.
		ct.currentMemBytes = readCgroupMemoryBytes()
	} else {
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			return
		}
		cpuTime := tvToDuration(usage.Utime) + tvToDuration(usage.Stime)
		cpuDeltaSec = (cpuTime - ct.lastCPU).Seconds()
		ct.lastCPU = cpuTime
	}

	// CPU% = cpu_time_used / (wall_time * allocated_cores) * 100
	// Identical formula to CloudWatch ECS CPUUtilization.
	pct := (cpuDeltaSec / elapsed / ct.allocatedCPU) * 100.0
	if pct > 100.0 {
		pct = 100.0
	}
	if pct < 0 {
		pct = 0
	}
	ct.currentPct = pct
	ct.lastTime = now
}

func (ct *cpuTrackerState) get() float64 {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return math.Round(ct.currentPct*100) / 100
}

// getMemoryBytes returns the latest container memory usage in bytes.
// Only populated when useECSMetadata is true; returns 0 otherwise.
func (ct *cpuTrackerState) getMemoryBytes() uint64 {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.currentMemBytes
}

func tvToDuration(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

// estimateCPUPercent returns actual CPU usage as percentage of allocated vCPU.
func estimateCPUPercent() float64 {
	return cpuTracker.get()
}
