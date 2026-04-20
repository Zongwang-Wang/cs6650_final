package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

type EventProducer struct {
	writer *kafka.Writer
}

func NewEventProducer(brokers []string, topic string) *EventProducer {
	return &EventProducer{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        topic,
			Balancer:     &kafka.LeastBytes{},
			BatchTimeout: 10 * time.Millisecond,
			WriteTimeout: 5 * time.Second,
			RequiredAcks: kafka.RequireOne,
		},
	}
}

func (p *EventProducer) PublishEvent(ctx context.Context, event ScalingDecision) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := p.writer.WriteMessages(ctx, kafka.Message{Value: data}); err != nil {
		log.Printf("publish scaling decision: %v", err)
		return err
	}
	return nil
}

func (p *EventProducer) Close() { p.writer.Close() }
