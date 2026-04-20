package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/segmentio/kafka-go"
)

// MetricEvent mirrors the struct published by cart-api.
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

// Consumer reads MetricEvents from Kafka and feeds them to the Evaluator.
type Consumer struct {
	reader    *kafka.Reader
	evaluator *Evaluator
	metrics   *Metrics
}

func NewConsumer(brokers []string, topic, group string, evaluator *Evaluator, metrics *Metrics) *Consumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  group,
		MinBytes: 1,
		MaxBytes: 10e6,
	})

	return &Consumer{
		reader:    r,
		evaluator: evaluator,
		metrics:   metrics,
	}
}

func (c *Consumer) Start(ctx context.Context) {
	log.Println("kafka consumer started")
	for {
		msg, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("consumer read error: %v", err)
			continue
		}

		c.metrics.MessagesConsumed.Inc()

		var event MetricEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			log.Printf("failed to unmarshal metric event: %v", err)
			continue
		}

		c.evaluator.Evaluate(event)
	}
}

func (c *Consumer) Close() {
	if err := c.reader.Close(); err != nil {
		log.Printf("failed to close kafka reader: %v", err)
	}
}
