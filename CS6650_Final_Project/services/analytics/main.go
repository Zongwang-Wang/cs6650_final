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
	port          := getEnv("PORT", "8081")
	brokers       := strings.Split(getEnv("KAFKA_BROKERS", "kafka:9092"), ",")
	consumerTopic := getEnv("KAFKA_CONSUMER_TOPIC", "album-metrics")
	producerTopic := getEnv("KAFKA_PRODUCER_TOPIC", "album-analytics-output")
	consumerGroup := getEnv("CONSUMER_GROUP", "analytics-group")

	metrics    := NewMetrics()
	producer   := NewProducer(brokers, producerTopic)
	aggregator := NewAggregator(producer, metrics)
	consumer   := NewConsumer(brokers, consumerTopic, consumerGroup, aggregator, metrics)

	ctx, cancel := context.WithCancel(context.Background())

	go consumer.Start(ctx)
	go aggregator.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("analytics listening on :%s (brokers=%v)", port, brokers)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("analytics server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("analytics shutting down...")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)

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
