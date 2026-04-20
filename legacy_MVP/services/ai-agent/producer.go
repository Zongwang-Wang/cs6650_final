package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

// EventProducer publishes scaling decisions to the infra-events Kafka topic.
type EventProducer struct {
	writer *kafka.Writer
}

// NewEventProducer creates a new Kafka writer for infrastructure events.
func NewEventProducer(brokers []string, topic string) *EventProducer {
	writer := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 100 * time.Millisecond,
		RequiredAcks: kafka.RequireOne,
	}

	log.Printf("Event producer initialized: topic=%s brokers=%v", topic, brokers)

	return &EventProducer{writer: writer}
}

// PublishEvent serializes and publishes a ScalingDecision to Kafka.
func (p *EventProducer) PublishEvent(ctx context.Context, event ScalingDecision) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	msg := kafka.Message{
		Key:   []byte(event.Timestamp),
		Value: data,
		Time:  time.Now(),
	}

	if err := p.writer.WriteMessages(ctx, msg); err != nil {
		return err
	}

	log.Printf("Published infra event: action=%s applied=%v task_count=%d->%d",
		event.Action, event.Applied, event.PreviousTaskCount, event.NewTaskCount)

	return nil
}

// Close shuts down the Kafka writer.
func (p *EventProducer) Close() error {
	return p.writer.Close()
}
