package backup

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ObjectUploader is the upload boundary used by the backup manager.
type ObjectUploader interface {
	Upload(ctx context.Context, localPath, key string) error
}

type S3Options struct {
	Bucket               string
	Prefix               string
	Region               string
	Endpoint             string
	ForcePathStyle       bool
	ServerSideEncryption string
	KMSKeyID             string
}

type S3Uploader struct {
	client *s3.Client
	opts   S3Options
}

func NewS3Uploader(ctx context.Context, opts S3Options) (*S3Uploader, error) {
	if strings.TrimSpace(opts.Bucket) == "" {
		return nil, fmt.Errorf("%w: S3 bucket is required", ErrInvalid)
	}
	loadOptions := make([]func(*awsconfig.LoadOptions) error, 0, 1)
	if opts.Region != "" {
		loadOptions = append(loadOptions, awsconfig.WithRegion(opts.Region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("backup: load AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		if opts.Endpoint != "" {
			options.BaseEndpoint = aws.String(opts.Endpoint)
		}
		options.UsePathStyle = opts.ForcePathStyle
	})
	return &S3Uploader{client: client, opts: opts}, nil
}

func (uploader *S3Uploader) Upload(ctx context.Context, localPath, key string) error {
	file, err := os.Open(localPath) //nolint:gosec // path is generated under the backup target
	if err != nil {
		return fmt.Errorf("backup: open archive for upload: %w", err)
	}
	defer func() { _ = file.Close() }()
	input := &s3.PutObjectInput{
		Bucket:      aws.String(uploader.opts.Bucket),
		Key:         aws.String(key),
		Body:        file,
		ContentType: aws.String("application/gzip"),
	}
	switch uploader.opts.ServerSideEncryption {
	case "AES256":
		input.ServerSideEncryption = s3types.ServerSideEncryptionAes256
	case "aws:kms":
		input.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(uploader.opts.KMSKeyID)
	}
	if _, err := uploader.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("backup: upload %s to s3://%s/%s: %w", localPath, uploader.opts.Bucket, key, err)
	}
	return nil
}
