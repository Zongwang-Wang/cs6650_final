package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus metrics for the alert service.
type Metrics struct {
	MessagesConsumed prometheus.Counter
	AlertsFired      *prometheus.CounterVec
	ActiveAlerts     *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		MessagesConsumed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "alert_messages_consumed_total",
			Help: "Total number of Kafka messages consumed",
		}),
		AlertsFired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alert_alerts_fired_total",
			Help: "Total number of alerts fired by type",
		}, []string{"type"}),
		ActiveAlerts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "alert_active_alerts",
			Help: "Currently active alerts by type",
		}, []string{"type"}),
	}

	prometheus.MustRegister(m.MessagesConsumed)
	prometheus.MustRegister(m.AlertsFired)
	prometheus.MustRegister(m.ActiveAlerts)

	return m
}
