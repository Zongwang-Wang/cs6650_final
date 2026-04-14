package main

import (
	"context"
	"encoding/json"
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
	port           := getEnv("PORT", "8082")
	brokers        := strings.Split(getEnv("KAFKA_BROKERS", "kafka:9092"), ",")
	topic          := getEnv("KAFKA_TOPIC", "album-metrics")
	group          := getEnv("CONSUMER_GROUP", "alert-group")
	snsARN         := getEnv("SNS_TOPIC_ARN", "")
	notifListPath  := getEnv("NOTIF_LIST", "/etc/alert/notifications.json")

	// Load notification config (team list + thresholds)
	cpuThresholdPct := 70.0 // default
	if data, err := os.ReadFile(notifListPath); err == nil {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err == nil {
			var th map[string]float64
			if err := json.Unmarshal(raw["thresholds"], &th); err == nil {
				if v, ok := th["cpu_alert_pct"]; ok {
					cpuThresholdPct = v
					log.Printf("loaded cpu_alert_pct=%.0f from %s", v, notifListPath)
				}
			}
		}
	}

	metrics   := NewMetrics()
	notifier  := NewNotifier(snsARN, notifListPath)
	evaluator := NewEvaluator(notifier, metrics)
	consumer  := NewConsumer(brokers, topic, group, evaluator, metrics)
	cpuWatcher := NewCPUWatcher(notifier, metrics, cpuThresholdPct)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go consumer.Start(ctx)
	go cpuWatcher.Start(ctx) // polls CloudWatch every 60s for EC2 CPU

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		log.Printf("alert service listening on :%s (brokers=%v, cpu_threshold=%.0f%%)", port, brokers, cpuThresholdPct)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("alert server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("alert service shutting down...")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
	consumer.Close()
	log.Println("alert service stopped")
}
