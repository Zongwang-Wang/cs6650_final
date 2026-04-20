package kafka

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/segmentio/kafka-go"
)

// MetricEvent is published to `album-metrics` after every HTTP request.
// Same shape as the original cart-api MetricEvent so analytics/alert services
// can be reused with minimal changes.
type MetricEvent struct {
	Timestamp  string `json:"timestamp"`
	Service    string `json:"service"`     // "album-store"
	InstanceID string `json:"instance_id"` // hostname / EC2 instance ID
	Endpoint   string `json:"endpoint"`    // route label e.g. "GET /albums/:id"
	Method     string `json:"method"`
	StatusCode int    `json:"status_code"`
	LatencyMs  int64  `json:"latency_ms"`
	CacheHit   bool   `json:"cache_hit"` // true when Redis served the response
}

// Producer wraps a kafka-go Writer. Errors are logged and never returned —
// Kafka is non-critical for the album store; a failure must not break requests.
type Producer struct {
	writer     *kafka.Writer
	instanceID string
	enabled    bool
}

// New creates a Producer. If brokers is empty or KAFKA_BROKERS env var is
// unset, the producer is disabled and all Publish calls are no-ops.
func New(brokers []string, topic string) *Producer {
	if len(brokers) == 0 {
		log.Println("kafka: no brokers configured — metrics producer disabled")
		return &Producer{enabled: false}
	}

	hostname, _ := os.Hostname()

	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
		WriteTimeout: 3 * time.Second,
		RequiredAcks: kafka.RequireOne,
		Async:        true, // fire-and-forget — never block the HTTP handler
	}

	log.Printf("kafka: producer ready → %v topic=%s", brokers, topic)
	return &Producer{
		writer:     w,
		instanceID: hostname,
		enabled:    true,
	}
}

// Publish sends a MetricEvent asynchronously.
// Uses Async=true writer so this call returns immediately and never adds
// latency to the HTTP request path.
func (p *Producer) Publish(endpoint, method string, statusCode int, latencyMs int64, cacheHit bool) {
	if !p.enabled {
		return
	}

	event := MetricEvent{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Service:    "album-store",
		InstanceID: p.instanceID,
		Endpoint:   endpoint,
		Method:     method,
		StatusCode: statusCode,
		LatencyMs:  latencyMs,
		CacheHit:   cacheHit,
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(p.instanceID),
		Value: data,
	}); err != nil {
		log.Printf("kafka: publish error: %v", err)
	}
}

func (p *Producer) Close() {
	if p.enabled && p.writer != nil {
		p.writer.Close()
	}
}
