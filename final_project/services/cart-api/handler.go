package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// burnCPU simulates realistic per-request CPU work.
// ~100 iterations ≈ 0.5ms CPU per request.
// At 200 req/s on 0.25 vCPU, this pushes CPU to ~40%.
// At 500 req/s, it hits ~80-100%.
func burnCPU() {
	data := []byte("simulate-cpu-intensive-work")
	for i := 0; i < 100; i++ {
		data = sha256.New().Sum(data)
	}
}

type AddItemRequest struct {
	ProductID  int `json:"product_id"`
	Quantity   int `json:"quantity"`
	CustomerID int `json:"customer_id"`
}

type AddItemResponse struct {
	CartID     string `json:"cart_id"`
	ItemsCount int    `json:"items_count"`
}

type Cart struct {
	ID    string
	Items []AddItemRequest
}

type Handler struct {
	mu       sync.RWMutex
	carts    map[int]*Cart // keyed by customer_id
	producer *KafkaProducer
	metrics  *Metrics
}

func NewHandler(producer *KafkaProducer, metrics *Metrics) *Handler {
	return &Handler{
		carts:    make(map[int]*Cart),
		producer: producer,
		metrics:  metrics,
	}
}

func (h *Handler) AddItem(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Simulate realistic CPU work (e.g., validation, serialization, DB lookup)
	// This burns ~2ms of CPU per request so the service hits 70-80% under load
	burnCPU()

	var req AddItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.metrics.RecordRequest("POST", "/cart/items", 400, time.Since(start))
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.ProductID == 0 || req.Quantity == 0 || req.CustomerID == 0 {
		h.metrics.RecordRequest("POST", "/cart/items", 400, time.Since(start))
		http.Error(w, `{"error":"product_id, quantity, and customer_id are required"}`, http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	cart, ok := h.carts[req.CustomerID]
	if !ok {
		cart = &Cart{ID: uuid.New().String()}
		h.carts[req.CustomerID] = cart
	}
	cart.Items = append(cart.Items, req)
	itemsCount := len(cart.Items)
	cartID := cart.ID
	h.mu.Unlock()

	latency := time.Since(start)
	statusCode := http.StatusCreated

	h.metrics.RecordRequest("POST", "/cart/items", statusCode, latency)

	go h.producer.PublishMetric("/cart/items", "POST", statusCode, latency)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(AddItemResponse{
		CartID:     cartID,
		ItemsCount: itemsCount,
	})
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))

	latency := time.Since(start)
	h.metrics.RecordRequest("GET", "/health", 200, latency)
	go h.producer.PublishMetric("/health", "GET", 200, latency)
}
