package config

import (
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL string
	RedisURL    string
	S3Bucket    string
	AWSRegion   string
	WorkerCount int
	MaxDBConns  int32
	Port        string
}

func Load() *Config {
	workerCount := 32
	if v := os.Getenv("WORKER_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			workerCount = n
		}
	}
	maxDBConns := int32(50)
	if v := os.Getenv("MAX_DB_CONNS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			maxDBConns = int32(n)
		}
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-west-2"
	}
	return &Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		RedisURL:    os.Getenv("REDIS_URL"),
		S3Bucket:    os.Getenv("S3_BUCKET"),
		AWSRegion:   region,
		WorkerCount: workerCount,
		MaxDBConns:  maxDBConns,
		Port:        port,
	}
}
