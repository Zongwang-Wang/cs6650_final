package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	MessagesConsumed prometheus.Counter
	AlertsFired      *prometheus.CounterVec
	ActiveAlerts     *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	return &Metrics{
		MessagesConsumed: promauto.NewCounter(prometheus.CounterOpts{
			Name: "alert_messages_consumed_total",
			Help: "Total MetricEvents consumed from album-metrics topic.",
		}),
		AlertsFired: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "alert_fired_total",
			Help: "Total alerts fired, by type.",
		}, []string{"type"}),
		ActiveAlerts: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "alert_active",
			Help: "Whether an alert type is currently firing (1=yes, 0=no).",
		}, []string{"type"}),
	}
}
