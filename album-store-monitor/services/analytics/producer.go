package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/segmentio/kafka-go"
)

type Producer struct {
	writer *kafka.Writer
}

func NewProducer(brokers []string, topic string) *Producer {
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
		WriteTimeout: 5 * time.Second,
		RequiredAcks: kafka.RequireOne,
	}
	return &Producer{writer: w}
}

func (p *Producer) Publish(agg AggregatedMetric) {
	data, err := json.Marshal(agg)
	if err != nil {
		log.Printf("analytics producer marshal: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.writer.WriteMessages(ctx, kafka.Message{Value: data}); err != nil {
		log.Printf("analytics producer write: %v", err)
	}
}

func (p *Producer) Close() { p.writer.Close() }
