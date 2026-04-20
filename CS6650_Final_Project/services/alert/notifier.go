package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// ── Notification list ─────────────────────────────────────────────────────────

type TeamMember struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type NotifConfig struct {
	Team []TeamMember `json:"team"`
}

func loadNotifConfig(path string) *NotifConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("notifier: cannot read %s: %v — using empty team list", path, err)
		return &NotifConfig{}
	}
	var cfg NotifConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("notifier: parse error %s: %v", path, err)
		return &NotifConfig{}
	}
	log.Printf("notifier: loaded %d team members from %s", len(cfg.Team), path)
	return &cfg
}

// ── Alert ─────────────────────────────────────────────────────────────────────

type Alert struct {
	Type      string
	Instance  string
	Value     float64
	Threshold float64
	Message   string
}

// ── Notifier ──────────────────────────────────────────────────────────────────

type Notifier struct {
	snsClient   *sns.Client
	snsTopicARN string
	team        []TeamMember
}

func NewNotifier(topicARN, notifListPath string) *Notifier {
	n := &Notifier{snsTopicARN: topicARN}

	// Load team notification list
	nc := loadNotifConfig(notifListPath)
	n.team = nc.Team

	if topicARN == "" {
		return n
	}
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(getEnv("AWS_REGION", "us-west-2")))
	if err != nil {
		log.Printf("notifier: AWS config error: %v", err)
		return n
	}
	n.snsClient = sns.NewFromConfig(cfg)
	return n
}

// Send fires an alert: logs to stdout (always) and publishes to SNS (if configured).
// The SNS topic should have all team email addresses subscribed.
func (n *Notifier) Send(a Alert) {
	ts := time.Now().UTC().Format(time.RFC3339)
	payload, _ := json.MarshalIndent(map[string]any{
		"timestamp": ts,
		"type":      a.Type,
		"instance":  a.Instance,
		"value":     a.Value,
		"threshold": a.Threshold,
		"message":   a.Message,
		"recipients": func() []string {
			emails := make([]string, len(n.team))
			for i, m := range n.team {
				emails[i] = fmt.Sprintf("%s <%s>", m.Name, m.Email)
			}
			return emails
		}(),
	}, "", "  ")

	// Always log — visible in docker logs alert
	log.Printf("🚨 ALERT [%s] instance=%s value=%.1f threshold=%.1f: %s",
		a.Type, a.Instance, a.Value, a.Threshold, a.Message)
	log.Printf("   Recipients: %v", func() []string {
		var r []string
		for _, m := range n.team {
			r = append(r, m.Name)
		}
		return r
	}())

	if n.snsClient == nil || n.snsTopicARN == "" {
		// No SNS: log full payload so it's visible for demo/testing
		log.Printf("   [SNS disabled] payload:\n%s", payload)
		return
	}

	subject := fmt.Sprintf("[Album Store Alert] %s on %s", a.Type, a.Instance)
	_, err := n.snsClient.Publish(context.Background(), &sns.PublishInput{
		TopicArn: aws.String(n.snsTopicARN),
		Subject:  aws.String(subject),
		Message:  aws.String(string(payload)),
	})
	if err != nil {
		log.Printf("notifier SNS error: %v", err)
	} else {
		log.Printf("   SNS notification sent to topic (all %d subscribers)", len(n.team))
	}
}
