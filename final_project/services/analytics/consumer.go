package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/segmentio/kafka-go"
)

// MetricEvent matches the struct published by cart-api.
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

type Consumer struct {
	reader     *kafka.Reader
	aggregator *Aggregator
	metrics    *Metrics
}

func NewConsumer(brokers []string, topic, group string, agg *Aggregator, m *Metrics) *Consumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  group,
		MinBytes: 1,
		MaxBytes: 10e6, // 10 MB
	})

	return &Consumer{
		reader:     r,
		aggregator: agg,
		metrics:    m,
	}
}

func (c *Consumer) Start(ctx context.Context) {
	log.Println("consumer started")
	for {
		msg, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("consumer read error: %v", err)
			continue
		}

		var event MetricEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			log.Printf("failed to unmarshal metric event: %v", err)
			continue
		}

		c.aggregator.Add(event)
		c.metrics.MessagesConsumed.Inc()
	}
}

func (c *Consumer) Close() {
	if err := c.reader.Close(); err != nil {
		log.Printf("failed to close kafka reader: %v", err)
	}
}
