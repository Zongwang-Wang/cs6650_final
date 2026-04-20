package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds all configuration loaded from environment variables.
type Config struct {
	Port               string
	KafkaBrokers       []string
	KafkaConsumerTopic string
	KafkaProducerTopic string
	ConsumerGroup      string
	AnthropicAPIKey    string
	TerraformDir       string
	DryRun             bool
	CooldownSeconds    int
	InstanceID         string
}

// AgentState holds the runtime state of the agent.
type AgentState struct {
	mu                sync.RWMutex
	LastDecision      *ScalingDecision
	CurrentTaskCount  int
	DryRun            bool
	LastApplyTime     time.Time
	MetricHistory     []AggregatedMetric
	MaxHistoryWindows int
}

// ScalingDecision is the final decision record.
type ScalingDecision struct {
	Timestamp         string  `json:"timestamp"`
	Action            string  `json:"action"`
	Reasoning         string  `json:"reasoning"`
	PreviousTaskCount int     `json:"previous_task_count"`
	NewTaskCount      int     `json:"new_task_count"`
	Confidence        float64 `json:"confidence"`
	Applied           bool    `json:"applied"`
	DryRun            bool    `json:"dry_run"`
}

func loadConfig() Config {
	port := getEnv("PORT", "8083")
	brokersStr := getEnv("KAFKA_BROKERS", "localhost:9092")
	brokers := strings.Split(brokersStr, ",")
	consumerTopic := getEnv("KAFKA_CONSUMER_TOPIC", "analytics-output-topic")
	producerTopic := getEnv("KAFKA_PRODUCER_TOPIC", "infra-events-topic")
	consumerGroup := getEnv("CONSUMER_GROUP", "ai-agent-group")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is required")
	}
	terraformDir := getEnv("TERRAFORM_DIR", "/terraform")
	dryRun := getEnv("DRY_RUN", "true") == "true"
	cooldown, err := strconv.Atoi(getEnv("COOLDOWN_SECONDS", "60"))
	if err != nil {
		cooldown = 60
	}
	instanceID := getEnv("INSTANCE_ID", "ai-agent-0")

	return Config{
		Port:               port,
		KafkaBrokers:       brokers,
		KafkaConsumerTopic: consumerTopic,
		KafkaProducerTopic: producerTopic,
		ConsumerGroup:      consumerGroup,
		AnthropicAPIKey:    apiKey,
		TerraformDir:       terraformDir,
		DryRun:             dryRun,
		CooldownSeconds:    cooldown,
		InstanceID:         instanceID,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting AI DevOps Agent...")

	cfg := loadConfig()

	state := &AgentState{
		CurrentTaskCount:  4, // default, will be read from tfvars on startup
		DryRun:            cfg.DryRun,
		MaxHistoryWindows: 5,
	}

	// Read current task count from terraform.tfvars
	currentCount, err := ReadCurrentTaskCount(cfg.TerraformDir)
	if err != nil {
		log.Printf("WARN: could not read current task count from tfvars: %v, using default 4", err)
	} else {
		state.CurrentTaskCount = currentCount
		MetricsCurrentTaskCount.Set(float64(currentCount))
	}

	log.Printf("Config: port=%s brokers=%v consumer_topic=%s producer_topic=%s dry_run=%v cooldown=%ds",
		cfg.Port, cfg.KafkaBrokers, cfg.KafkaConsumerTopic, cfg.KafkaProducerTopic, cfg.DryRun, cfg.CooldownSeconds)

	// Initialize Kafka producer
	producer := NewEventProducer(cfg.KafkaBrokers, cfg.KafkaProducerTopic)
	defer producer.Close()

	// Initialize Claude client
	claudeClient := NewClaudeClient(cfg.AnthropicAPIKey)

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Kafka consumer
	go StartConsumer(ctx, cfg, state, claudeClient, producer)

	// HTTP server for Prometheus metrics and status
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		state.mu.RLock()
		defer state.mu.RUnlock()

		status := map[string]interface{}{
			"current_task_count": state.CurrentTaskCount,
			"dry_run":            state.DryRun,
			"instance_id":        cfg.InstanceID,
			"history_windows":    len(state.MetricHistory),
		}
		if state.LastDecision != nil {
			status["last_decision"] = state.LastDecision
		}
		if !state.LastApplyTime.IsZero() {
			status["last_apply_time"] = state.LastApplyTime.Format(time.RFC3339)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	go func() {
		log.Printf("HTTP server listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down...", sig)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("AI Agent stopped.")
}
