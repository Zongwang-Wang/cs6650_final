package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

type Producer struct {
	writer     *kafka.Writer
	instanceID string
}

func NewProducer(brokers []string, topic, instanceID string) *Producer {
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
		WriteTimeout: 5 * time.Second,
		RequiredAcks: kafka.RequireOne,
		Async:        false,
	}

	return &Producer{
		writer:     w,
		instanceID: instanceID,
	}
}

func (p *Producer) Publish(agg AggregatedMetric) {
	data, err := json.Marshal(agg)
	if err != nil {
		log.Printf("failed to marshal aggregated metric: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(p.instanceID),
		Value: data,
	})
	if err != nil {
		log.Printf("failed to publish aggregated metric: %v", err)
	}
}

func (p *Producer) Close() {
	if err := p.writer.Close(); err != nil {
		log.Printf("failed to close kafka writer: %v", err)
	}
}
