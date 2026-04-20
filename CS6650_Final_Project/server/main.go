package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"album-store/cache"
	"album-store/config"
	"album-store/db"
	"album-store/handler"
	albumkafka "album-store/kafka"
	s3client "album-store/s3"
	"album-store/store"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	log.Println("Connecting to PostgreSQL...")
	pool, err := db.New(ctx, cfg.DatabaseURL, cfg.MaxDBConns)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()
	log.Println("PostgreSQL ready")

	log.Println("Connecting to Redis...")
	redisCache, err := cache.New(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	log.Println("Redis ready")

	log.Println("Connecting to S3...")
	s3c, err := s3client.New(ctx, cfg.S3Bucket, cfg.AWSRegion)
	if err != nil {
		log.Fatalf("s3: %v", err)
	}
	log.Println("S3 ready")

	// Kafka producer — non-critical, disabled if KAFKA_BROKERS not set.
	// Publishes one MetricEvent per HTTP request to the album-metrics topic.
	// The analytics + alert services consume from this topic.
	kafkaBrokers := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	if len(kafkaBrokers) == 1 && kafkaBrokers[0] == "" {
		kafkaBrokers = nil
	}
	kafkaTopic := os.Getenv("KAFKA_TOPIC")
	if kafkaTopic == "" {
		kafkaTopic = "album-metrics"
	}
	kafkaProducer := albumkafka.New(kafkaBrokers, kafkaTopic)
	defer kafkaProducer.Close()

	albumStore := store.NewAlbumStore(pool)
	photoStore := store.NewPhotoStore(pool)

	log.Println("Warming seq counters...")
	seqMaxes, err := albumStore.LoadSeqMaxes(ctx)
	if err != nil {
		log.Printf("WARN warm seq: %v", err)
	} else {
		for albumID, maxSeq := range seqMaxes {
			redisCache.InitSeq(ctx, albumID, maxSeq)
		}
		log.Printf("Warmed %d album seq counters", len(seqMaxes))
	}

	albumHandler := handler.NewAlbumHandler(albumStore, redisCache)
	photoHandler := handler.NewPhotoHandler(photoStore, albumStore, redisCache, s3c)

	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(handler.MetricsMiddleware(kafkaProducer)) // publishes MetricEvent to Kafka

	r.Get("/health", handler.Health)
	r.Get("/metrics", promhttp.Handler().ServeHTTP)
	r.Put("/albums/{album_id}", albumHandler.Put)
	r.Get("/albums/{album_id}", albumHandler.Get)
	r.Get("/albums", albumHandler.List)
	r.Post("/albums/{album_id}/photos", photoHandler.Upload)
	r.Get("/albums/{album_id}/photos/{photo_id}", photoHandler.Get)
	r.Delete("/albums/{album_id}/photos/{photo_id}", photoHandler.Delete)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       300 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	log.Printf("Server listening on :%s (kafka=%v)", cfg.Port, kafkaBrokers)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
