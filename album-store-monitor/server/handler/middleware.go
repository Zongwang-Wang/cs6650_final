package handler

import (
	"net/http"
	"strings"
	"time"

	"album-store/kafka"
)

// contextKey for passing cache-hit signal from cache layer to middleware.
type contextKey string

const CacheHitKey contextKey = "cache_hit"

// responseRecorder wraps http.ResponseWriter to capture the status code.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

// MetricsMiddleware wraps every route and publishes a MetricEvent to Kafka
// after the handler completes. The middleware is transparent: it adds no
// latency to the response path because the Kafka producer uses Async=true.
func MetricsMiddleware(producer *kafka.Producer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rr := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rr, r)

			// Derive a stable endpoint label from the URL path.
			// Replace UUIDs with :id so cardinality stays low in Kafka/Prometheus.
			endpoint := labelFor(r.Method, r.URL.Path)

			// Cache hit is passed via response header set by handlers that check Redis.
			// Handlers set X-Cache: HIT when they serve from Redis.
			cacheHit := rr.Header().Get("X-Cache") == "HIT"

			producer.Publish(
				endpoint,
				r.Method,
				rr.status,
				time.Since(start).Milliseconds(),
				cacheHit,
			)
		})
	}
}

// labelFor collapses dynamic path segments into stable labels.
// e.g. "GET /albums/a1b2c3d4-.../photos/f1e2d3c4-..." → "GET /albums/:id/photos/:id"
func labelFor(method, path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	out := make([]string, len(parts))
	for i, p := range parts {
		if len(p) == 36 && strings.Count(p, "-") == 4 {
			out[i] = ":id" // UUID pattern
		} else {
			out[i] = p
		}
	}
	return method + " /" + strings.Join(out, "/")
}
