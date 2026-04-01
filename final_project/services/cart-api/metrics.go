package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	RequestTotal      *prometheus.CounterVec
	RequestDuration   *prometheus.HistogramVec
	KafkaPublishTotal *prometheus.CounterVec
	CPUPercent        prometheus.Gauge
	MemoryBytes       prometheus.Gauge // container RSS bytes from ECS Task Metadata (0 on non-ECS)
}

func NewMetrics() *Metrics {
	m := &Metrics{
		RequestTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cart_api_http_requests_total",
				Help: "Total number of HTTP requests",
			},
			[]string{"method", "endpoint", "status"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cart_api_http_request_duration_seconds",
				Help:    "HTTP request duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "endpoint", "status"},
		),
		KafkaPublishTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cart_api_kafka_publish_total",
				Help: "Total number of Kafka publish attempts",
			},
			[]string{"result"},
		),
		CPUPercent: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "cart_api_cpu_percent",
				Help: "Current CPU usage as percentage of allocated vCPU",
			},
		),
		MemoryBytes: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "cart_api_memory_bytes",
				Help: "Container RSS memory usage in bytes (from ECS Task Metadata; 0 on non-ECS)",
			},
		),
	}

	prometheus.MustRegister(m.RequestTotal)
	prometheus.MustRegister(m.RequestDuration)
	prometheus.MustRegister(m.KafkaPublishTotal)
	prometheus.MustRegister(m.CPUPercent)
	prometheus.MustRegister(m.MemoryBytes)

	return m
}

func (m *Metrics) RecordRequest(method, endpoint string, status int, duration time.Duration) {
	statusStr := strconv.Itoa(status)
	m.RequestTotal.WithLabelValues(method, endpoint, statusStr).Inc()
	m.RequestDuration.WithLabelValues(method, endpoint, statusStr).Observe(duration.Seconds())
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}
