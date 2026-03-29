package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type DynamoWriter struct {
	client    *dynamodb.Client
	tableName string
}

func NewDynamoWriter() (*DynamoWriter, error) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return &DynamoWriter{
		client:    dynamodb.NewFromConfig(cfg),
		tableName: os.Getenv("DYNAMODB_TABLE"),
	}, nil
}

func (d *DynamoWriter) PutMetric(agg AggregatedMetric) {
	item, err := attributevalue.MarshalMap(agg)
	if err != nil {
		log.Printf("dynamo marshal error: %v", err)
		return
	}
	_, err = d.client.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: &d.tableName,
		Item:      item,
	})
	if err != nil {
		log.Printf("dynamo put error: %v", err)
	}
}
