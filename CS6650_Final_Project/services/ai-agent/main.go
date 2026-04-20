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
}

type AgentState struct {
	mu                sync.RWMutex
	LastDecision      *ScalingDecision
	CurrentNodeCount  int
	LastApplyTime     time.Time
	MetricHistory     []AggregatedMetric
	MaxHistoryWindows int
}

// ScalingDecision is the event published to album-infra-events topic.
type ScalingDecision struct {
	Timestamp         string  `json:"timestamp"`
	Action            string  `json:"action"`
	Reasoning         string  `json:"reasoning"`
	PreviousNodeCount int     `json:"previous_node_count"`
	NewNodeCount      int     `json:"new_node_count"`
	Confidence        float64 `json:"confidence"`
	Applied           bool    `json:"applied"`
	DryRun            bool    `json:"dry_run"`
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() Config {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is required")
	}
	cooldown, _ := strconv.Atoi(getEnv("COOLDOWN_SECONDS", "120"))
	return Config{
		Port:               getEnv("PORT", "8083"),
		KafkaBrokers:       strings.Split(getEnv("KAFKA_BROKERS", "kafka:9092"), ","),
		KafkaConsumerTopic: getEnv("KAFKA_CONSUMER_TOPIC", "album-analytics-output"),
		KafkaProducerTopic: getEnv("KAFKA_PRODUCER_TOPIC", "album-infra-events"),
		ConsumerGroup:      getEnv("CONSUMER_GROUP", "ai-agent-group"),
		AnthropicAPIKey:    apiKey,
		TerraformDir:       getEnv("TERRAFORM_DIR", "/terraform"),
		DryRun:             getEnv("DRY_RUN", "true") == "true",
		CooldownSeconds:    cooldown,
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Album Store AI DevOps Agent...")

	cfg := loadConfig()

	// Try to read current node count from Terraform state
	currentNodes := 4 // default — our deployed cluster
	if count, err := ReadCurrentNodeCount(cfg.TerraformDir); err == nil {
		currentNodes = count
	} else {
		log.Printf("WARN: could not read node count from terraform: %v, using default %d", err, currentNodes)
	}

	state := &AgentState{
		CurrentNodeCount:  currentNodes,
		MaxHistoryWindows: 5,
	}
	MetricsCurrentNodeCount.Set(float64(currentNodes))

	log.Printf("Config: brokers=%v topic_in=%s topic_out=%s dry_run=%v cooldown=%ds",
		cfg.KafkaBrokers, cfg.KafkaConsumerTopic, cfg.KafkaProducerTopic,
		cfg.DryRun, cfg.CooldownSeconds)

	producer := NewEventProducer(cfg.KafkaBrokers, cfg.KafkaProducerTopic)
	defer producer.Close()

	claude := NewClaudeClient(cfg.AnthropicAPIKey)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go StartConsumer(ctx, cfg, state, claude, producer)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		state.mu.RLock()
		defer state.mu.RUnlock()
		status := map[string]any{
			"current_node_count": state.CurrentNodeCount,
			"dry_run":            cfg.DryRun,
			"history_windows":    len(state.MetricHistory),
		}
		if state.LastDecision != nil {
			status["last_decision"] = state.LastDecision
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: mux}
	go func() {
		log.Printf("AI agent HTTP on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP error: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down AI agent...")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
	log.Println("AI agent stopped.")
}
