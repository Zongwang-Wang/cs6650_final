package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// Alert represents a triggered alert.
type Alert struct {
	Type      string  `json:"type"`
	Instance  string  `json:"instance"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Message   string  `json:"message"`
}

// alertLog is the structured JSON format written to stdout.
type alertLog struct {
	Level     string  `json:"level"`
	Type      string  `json:"type"`
	Instance  string  `json:"instance"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Timestamp string  `json:"timestamp"`
	Message   string  `json:"message"`
}

// Notifier sends alert notifications.
type Notifier struct {
	snsClient *sns.Client
	topicARN  string
}

func NewNotifier(topicARN string) *Notifier {
	n := &Notifier{topicARN: topicARN}
	if topicARN != "" {
		cfg, err := config.LoadDefaultConfig(context.Background())
		if err != nil {
			log.Printf("failed to load AWS config: %v — SNS notifications disabled", err)
			return n
		}
		n.snsClient = sns.NewFromConfig(cfg)
		log.Printf("SNS notifier configured for topic %s", topicARN)
	}
	return n
}

// Send outputs the alert to stdout and, if configured, publishes to SNS.
func (n *Notifier) Send(alert Alert) {
	// Always log to stdout.
	n.logToStdout(alert)

	// If SNS is configured, also publish.
	if n.snsClient != nil {
		n.publishToSNS(alert)
	}
}

func (n *Notifier) logToStdout(alert Alert) {
	entry := alertLog{
		Level:     "ALERT",
		Type:      alert.Type,
		Instance:  alert.Instance,
		Value:     alert.Value,
		Threshold: alert.Threshold,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   alert.Message,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("failed to marshal alert log: %v", err)
		return
	}
	// Write raw JSON line to stdout.
	log.Println(string(data))
}

func (n *Notifier) publishToSNS(alert Alert) {
	subject := fmt.Sprintf("[CS6650 Alert] %s on %s", alert.Type, alert.Instance)
	message := fmt.Sprintf(
		"Alert Type: %s\nInstance: %s\nValue: %.2f\nThreshold: %.2f\nMessage: %s\nTimestamp: %s",
		alert.Type, alert.Instance, alert.Value, alert.Threshold,
		alert.Message, time.Now().UTC().Format(time.RFC3339),
	)

	_, err := n.snsClient.Publish(context.Background(), &sns.PublishInput{
		TopicArn: aws.String(n.topicARN),
		Subject:  aws.String(subject),
		Message:  aws.String(message),
	})
	if err != nil {
		log.Printf("failed to publish alert to SNS: %v", err)
		return
	}
	log.Printf("alert published to SNS topic %s", n.topicARN)
}
