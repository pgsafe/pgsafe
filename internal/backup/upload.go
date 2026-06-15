package backup

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/pgsafe/pgsafe/internal/config"
)

const uploadConcurrency = 1

type s3Uploader struct {
	mgr    *manager.Uploader
	bucket string
}

func newS3Uploader(ctx context.Context, cfg *config.Config) (*s3Uploader, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		o.UsePathStyle = cfg.S3PathStyle
	})

	mgr := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = int64(cfg.S3MultipartPartSizeMB) * 1024 * 1024
		u.Concurrency = uploadConcurrency
	})

	return &s3Uploader{mgr: mgr, bucket: cfg.S3Bucket}, nil
}

// upload streams r to S3 under the given key and returns the number of bytes uploaded.
func (u *s3Uploader) upload(ctx context.Context, r io.Reader, s3Key string) (int64, error) {
	counter := &countingReader{r: r}

	slog.Info("uploading to S3", "key", s3Key, "bucket", u.bucket)

	_, err := u.mgr.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(u.bucket),
		Key:         aws.String(s3Key),
		Body:        counter,
		ContentType: aws.String("application/octet-stream"),
	})
	if err != nil {
		return counter.n, fmt.Errorf("s3 upload %s: %w", s3Key, err)
	}

	slog.Info("upload complete", "key", s3Key, "bytes", counter.n)
	return counter.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
