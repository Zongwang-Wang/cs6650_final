package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// CPUWatcher polls CloudWatch every 60s for EC2 CPU metrics and fires alerts
// when any instance exceeds the threshold (default 70% from notifications.json).
// This runs independently of the Kafka consumer loop — CPU data comes from
// CloudWatch, not from per-request MetricEvents (no CPU in album-store events).
type CPUWatcher struct {
	cw        *cloudwatch.Client
	notifier  *Notifier
	metrics   *Metrics
	threshold float64
}

func NewCPUWatcher(n *Notifier, m *Metrics, threshold float64) *CPUWatcher {
	region := getEnv("AWS_REGION", "us-west-2")
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
	if err != nil {
		log.Printf("cpu watcher: AWS config error: %v — CPU alerts disabled", err)
		return nil
	}
	log.Printf("cpu watcher: enabled (threshold=%.0f%%, region=%s)", threshold, region)
	return &CPUWatcher{
		cw:        cloudwatch.NewFromConfig(cfg),
		notifier:  n,
		metrics:   m,
		threshold: threshold,
	}
}

// Start polls CloudWatch every 60s. Non-blocking — call as a goroutine.
func (w *CPUWatcher) Start(ctx context.Context) {
	if w == nil {
		return
	}
	// Initial wait so Kafka consumer starts first
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	tagFilter := getEnv("EC2_TAG_FILTER", "album-store-node")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkCPU(ctx, tagFilter)
		}
	}
}

func (w *CPUWatcher) checkCPU(ctx context.Context, tagFilter string) {
	now := time.Now()
	start := now.Add(-2 * time.Minute)

	// Query CloudWatch EC2 CPUUtilization for all album-store nodes
	out, err := w.cw.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/EC2"),
		MetricName: aws.String("CPUUtilization"),
		Dimensions: []types.Dimension{},
		StartTime:  aws.Time(start),
		EndTime:    aws.Time(now),
		Period:     aws.Int32(60),
		Statistics: []types.Statistic{types.StatisticMaximum},
	})
	if err != nil {
		log.Printf("cpu watcher: CloudWatch error: %v", err)
		return
	}

	// Find peak CPU across datapoints
	var maxCPU float64
	var instanceID string
	for _, dp := range out.Datapoints {
		if dp.Maximum != nil && *dp.Maximum > maxCPU {
			maxCPU = *dp.Maximum
			instanceID = "ec2-cluster"
		}
	}

	if maxCPU == 0 {
		return // no data yet
	}

	log.Printf("cpu watcher: current max CPU = %.1f%% (threshold=%.0f%%)", maxCPU, w.threshold)

	if maxCPU > w.threshold {
		// Build meaningful instance tag from environment
		tag := os.Getenv("EC2_CLUSTER_NAME")
		if tag == "" {
			tag = tagFilter
		}
		if strings.TrimSpace(instanceID) == "" {
			instanceID = tag
		}

		w.notifier.Send(Alert{
			Type:      "HIGH_CPU",
			Instance:  instanceID,
			Value:     maxCPU,
			Threshold: w.threshold,
			Message:   fmt.Sprintf("EC2 cluster CPU at %.1f%% exceeds %.0f%% threshold — consider scaling or checking for runaway goroutines", maxCPU, w.threshold),
		})
		w.metrics.AlertsFired.WithLabelValues("HIGH_CPU").Inc()
		w.metrics.ActiveAlerts.WithLabelValues("HIGH_CPU").Set(1)
	} else {
		w.metrics.ActiveAlerts.WithLabelValues("HIGH_CPU").Set(0)
	}
}
