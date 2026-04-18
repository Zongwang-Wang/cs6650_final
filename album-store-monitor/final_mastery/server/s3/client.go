package s3client

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Client struct {
	s3     *s3.Client
	bucket string
	region string
}

func New(ctx context.Context, bucket, region string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &Client{
		s3:     s3.NewFromConfig(cfg),
		bucket: bucket,
		region: region,
	}, nil
}

// StreamUpload uploads a file to S3 using multipart upload.
// Reads from r directly without buffering the entire file in memory.
// Returns the public URL of the uploaded object.
func (c *Client) StreamUpload(ctx context.Context, key string, r io.Reader) (string, error) {
	uploader := manager.NewUploader(c.s3, func(u *manager.Uploader) {
		// 16MB parts: for 200MB file = 13 parts; fewer parts = fewer round trips vs. smaller parts
		// Concurrency=5: parallel part uploads per file; balanced so many concurrent files
		// don't overwhelm the S3 connection pool
		u.PartSize    = 16 * 1024 * 1024
		u.Concurrency = 5
		u.LeavePartsOnError = false
	})

	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   r,
	})
	if err != nil {
		return "", fmt.Errorf("s3 upload %s: %w", key, err)
	}

	// Construct public URL directly — avoids path-style URL issues from result.Location
	url := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", c.bucket, c.region, key)
	return url, nil
}

// Delete removes an object from S3. Returns nil if object doesn't exist (idempotent).
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return err
}
