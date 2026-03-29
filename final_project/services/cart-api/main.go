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
	port := getEnv("PORT", "8080")
	kafkaBrokers := strings.Split(getEnv("KAFKA_BROKERS", "kafka-1:9092,kafka-2:9092,kafka-3:9092"), ",")
	kafkaTopic := getEnv("KAFKA_TOPIC", "metrics-topic")
	instanceID := getEnv("INSTANCE_ID", getHostname())

	producer := NewKafkaProducer(kafkaBrokers, kafkaTopic, instanceID)
	defer producer.Close()

	metrics := NewMetrics()
	producer.SetMetrics(metrics)

	mux := http.NewServeMux()
	handler := NewHandler(producer, metrics)
	mux.HandleFunc("POST /cart/items", handler.AddItem)
	mux.HandleFunc("GET /health", handler.Health)
	mux.Handle("GET /metrics", metrics.Handler())

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("cart-api starting on :%s (instance=%s)", port, instanceID)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}
	log.Println("server stopped")
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
