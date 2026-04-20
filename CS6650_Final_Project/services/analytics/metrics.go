package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	MessagesConsumed prometheus.Counter
	AggregationTotal prometheus.Counter
	AvgLatency       prometheus.Gauge
	RequestRate      prometheus.Gauge
	CacheHitRate     prometheus.Gauge
	WindowSize       prometheus.Gauge
}

func NewMetrics() *Metrics {
	return &Metrics{
		MessagesConsumed: promauto.NewCounter(prometheus.CounterOpts{
			Name: "analytics_messages_consumed_total",
			Help: "Total MetricEvents consumed from album-metrics topic.",
		}),
		AggregationTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "analytics_aggregations_total",
			Help: "Total aggregation cycles completed.",
		}),
		AvgLatency: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "analytics_avg_latency_ms",
			Help: "Average request latency from last 60s window.",
		}),
		RequestRate: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "analytics_request_rate_per_sec",
			Help: "Request rate over last 60s window.",
		}),
		CacheHitRate: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "analytics_cache_hit_rate_pct",
			Help: "Redis cache hit rate for GET requests, last 60s window.",
		}),
		WindowSize: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "analytics_window_size",
			Help: "Number of events in the current 60s rolling window.",
		}),
	}
}

func (m *Metrics) Handler() http.Handler { return promhttp.Handler() }
