package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	MessagesConsumed prometheus.Counter
	AggregationTotal prometheus.Counter
	WindowSize       prometheus.Gauge
	AvgCPU           prometheus.Gauge
	RequestRate      prometheus.Gauge
}

func NewMetrics() *Metrics {
	m := &Metrics{
		MessagesConsumed: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "analytics_messages_consumed_total",
				Help: "Total number of metric events consumed from Kafka",
			},
		),
		AggregationTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "analytics_aggregations_total",
				Help: "Total number of aggregation computations performed",
			},
		),
		WindowSize: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "analytics_window_size",
				Help: "Current number of events in the sliding window",
			},
		),
		AvgCPU: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "analytics_avg_cpu_percent",
				Help: "Current average CPU percent across instances",
			},
		),
		RequestRate: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "analytics_request_rate_per_sec",
				Help: "Current request rate per second across instances",
			},
		),
	}

	prometheus.MustRegister(m.MessagesConsumed)
	prometheus.MustRegister(m.AggregationTotal)
	prometheus.MustRegister(m.WindowSize)
	prometheus.MustRegister(m.AvgCPU)
	prometheus.MustRegister(m.RequestRate)

	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}
