package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/segmentio/kafka-go"
)

// MetricEvent matches the struct published by the album store server middleware.
type MetricEvent struct {
	Timestamp  string `json:"timestamp"`
	Service    string `json:"service"`
	InstanceID string `json:"instance_id"`
	Endpoint   string `json:"endpoint"`
	Method     string `json:"method"`
	StatusCode int    `json:"status_code"`
	LatencyMs  int64  `json:"latency_ms"`
	CacheHit   bool   `json:"cache_hit"`
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
		MaxBytes: 10e6,
	})
	return &Consumer{reader: r, aggregator: agg, metrics: m}
}

func (c *Consumer) Start(ctx context.Context) {
	log.Println("analytics consumer started")
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
			log.Printf("unmarshal error: %v", err)
			continue
		}

		c.aggregator.Add(event)
		c.metrics.MessagesConsumed.Inc()
	}
}

func (c *Consumer) Close() { c.reader.Close() }
