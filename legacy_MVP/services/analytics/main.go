package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	port := getEnv("PORT", "8081")
	kafkaBrokers := strings.Split(getEnv("KAFKA_BROKERS", "kafka-1:9092,kafka-2:9092,kafka-3:9092"), ",")
	consumerTopic := getEnv("KAFKA_CONSUMER_TOPIC", "metrics-topic")
	producerTopic := getEnv("KAFKA_PRODUCER_TOPIC", "analytics-output-topic")
	consumerGroup := getEnv("CONSUMER_GROUP", "analytics-group")
	instanceID := getEnv("INSTANCE_ID", getHostname())

	var dynamo *DynamoWriter
	if table := getEnv("DYNAMODB_TABLE", ""); table != "" {
		dw, err := NewDynamoWriter()
		if err != nil {
			log.Printf("WARNING: failed to create DynamoDB writer: %v", err)
		} else {
			dynamo = dw
			log.Printf("DynamoDB persistence enabled (table=%s)", table)
		}
	}

	metrics := NewMetrics()
	producer := NewProducer(kafkaBrokers, producerTopic, instanceID)
	aggregator := NewAggregator(producer, metrics, dynamo)
	consumer := NewConsumer(kafkaBrokers, consumerTopic, consumerGroup, aggregator, metrics)

	ctx, cancel := context.WithCancel(context.Background())

	go consumer.Start(ctx)
	go aggregator.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("GET /metrics", metrics.Handler())

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("analytics starting on :%s (instance=%s)", port, instanceID)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}

	consumer.Close()
	producer.Close()
	log.Println("analytics stopped")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
