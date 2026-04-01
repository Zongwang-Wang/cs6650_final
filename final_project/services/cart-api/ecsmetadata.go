package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// ecsStatsURL returns the ECS Task Metadata Endpoint v4 stats URL.
// Each Fargate task exposes a unique task-scoped URL via the
// ECS_CONTAINER_METADATA_URI_V4 environment variable; the /stats suffix
// returns Docker-compatible CPU + memory stats for every container in the task.
// Returns "" when running outside ECS (env var not set).
func ecsStatsURL() string {
	base := os.Getenv("ECS_CONTAINER_METADATA_URI_V4")
	if base == "" {
		return ""
	}
	return base + "/stats"
}

// ecsContainerStats is the per-container subset of the Docker stats JSON
// returned by the Task Metadata Endpoint. The response is a
// map[containerID]ecsContainerStats covering all containers in the task.
type ecsContainerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"` // cumulative CPU nanoseconds
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"` // host-wide CPU nanoseconds
		OnlineCPUs     int    `json:"online_cpus"`
		ThrottlingData struct {
			ThrottledPeriods uint64 `json:"throttled_periods"`
			ThrottledTime    uint64 `json:"throttled_time"` // nanoseconds throttled
		} `json:"throttling_data"`
	} `json:"cpu_stats"`
	// PreCPUStats is the snapshot taken ~1 second before CPUStats.
	// Docker pre-computes the delta window for us, so we don't need to
	// maintain state between calls.
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"` // current RSS bytes
		Limit uint64 `json:"limit"` // container memory limit bytes
	} `json:"memory_stats"`
}

// ECSTaskStats holds the aggregated result for the entire ECS task.
type ECSTaskStats struct {
	CPUPercent       float64 // % of allocated vCPU (same formula as CloudWatch)
	MemoryBytes      uint64  // total RSS across all containers in the task
	ThrottledPercent float64 // % of scheduling periods that were throttled
}

// httpClient is a shared client with a short timeout so a slow metadata
// endpoint never blocks the sampling goroutine for more than 2 seconds.
var ecsHTTPClient = &http.Client{Timeout: 2 * time.Second}

// ReadECSTaskStats fetches live CPU and memory stats from the ECS Task Metadata
// Endpoint v4. It returns (stats, true) on success, (nil, false) when the
// endpoint is unavailable (e.g., local development outside ECS).
//
// CPU formula (identical to CloudWatch ECS CPUUtilization):
//
//	cpu% = (container_cpu_delta / system_cpu_delta) * online_cpus / allocated_cores * 100
//
// The stats are aggregated across ALL containers in the task so that
// sidecar processes (e.g., the Fargate networking pause container) are
// included — matching CloudWatch's task-level granularity.
func ReadECSTaskStats(allocatedCPU float64) (*ECSTaskStats, bool) {
	url := ecsStatsURL()
	if url == "" {
		return nil, false
	}
	resp, err := ecsHTTPClient.Get(url)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	var raw map[string]ecsContainerStats
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, false
	}
	if len(raw) == 0 {
		return nil, false
	}

	var (
		totalCPUDelta    uint64
		totalSystemDelta uint64
		totalMemory      uint64
		totalThrottled   uint64
		totalPeriods     uint64
		onlineCPUs       = 2 // conservative default for Fargate
	)

	for _, c := range raw {
		cpuDelta := c.CPUStats.CPUUsage.TotalUsage - c.PreCPUStats.CPUUsage.TotalUsage
		sysDelta := c.CPUStats.SystemCPUUsage - c.PreCPUStats.SystemCPUUsage
		totalCPUDelta += cpuDelta
		// system_cpu_usage is host-wide (same value for all containers); take
		// the last non-zero value rather than summing.
		if sysDelta > totalSystemDelta {
			totalSystemDelta = sysDelta
		}
		totalMemory += c.MemoryStats.Usage
		totalThrottled += c.CPUStats.ThrottlingData.ThrottledTime
		totalPeriods += c.CPUStats.ThrottlingData.ThrottledPeriods
		if c.CPUStats.OnlineCPUs > 0 {
			onlineCPUs = c.CPUStats.OnlineCPUs
		}
	}

	if totalSystemDelta == 0 || allocatedCPU <= 0 {
		return nil, false
	}

	cpuPct := float64(totalCPUDelta) / float64(totalSystemDelta) *
		float64(onlineCPUs) / allocatedCPU * 100.0
	if cpuPct > 100.0 {
		cpuPct = 100.0
	}
	if cpuPct < 0 {
		cpuPct = 0
	}

	var throttledPct float64
	if totalPeriods > 0 {
		throttledPct = float64(totalThrottled) / float64(totalPeriods*uint64(time.Millisecond)) * 100.0
	}

	return &ECSTaskStats{
		CPUPercent:       cpuPct,
		MemoryBytes:      totalMemory,
		ThrottledPercent: throttledPct,
	}, true
}
