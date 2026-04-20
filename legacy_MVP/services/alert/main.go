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

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	port := getEnv("PORT", "8082")
	brokersStr := getEnv("KAFKA_BROKERS", "localhost:9092")
	topic := getEnv("KAFKA_TOPIC", "metrics-topic")
	group := getEnv("CONSUMER_GROUP", "alert-group")
	snsTopicARN := getEnv("SNS_TOPIC_ARN", "")
	instanceID := getEnv("INSTANCE_ID", "alert-1")

	brokers := strings.Split(brokersStr, ",")

	metrics := NewMetrics()
	notifier := NewNotifier(snsTopicARN)
	evaluator := NewEvaluator(notifier, metrics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumer := NewConsumer(brokers, topic, group, evaluator, metrics)
	go consumer.Start(ctx)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go func() {
		log.Printf("alert service %s listening on :%s", instanceID, port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("shutting down alert service...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http server shutdown error: %v", err)
	}

	consumer.Close()
	log.Println("alert service stopped")
}
