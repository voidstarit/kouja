package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ObjectUploader is the upload and retention boundary used by the backup manager.
type ObjectUploader interface {
	Upload(ctx context.Context, localPath, key string) error
	DeleteBefore(ctx context.Context, cutoff time.Time) error
}

type S3Options struct {
	Bucket               string
	Prefix               string
	Region               string
	Endpoint             string
	ForcePathStyle       bool
	ServerSideEncryption string
	KMSKeyID             string
	PerBackupFolder      bool
	RetentionDays        int
}

type S3Uploader struct {
	client *s3.Client
	opts   S3Options
}

// RemoteObject describes an S3 object returned by List.
type RemoteObject struct {
	Key          string
	LastModified time.Time
	Size         int64
}

func NewS3Uploader(ctx context.Context, opts S3Options) (*S3Uploader, error) {
	if strings.TrimSpace(opts.Bucket) == "" {
		return nil, fmt.Errorf("%w: S3 bucket is required", ErrInvalid)
	}
	if opts.RetentionDays <= 0 {
		opts.RetentionDays = 7
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
		ContentType: aws.String("application/octet-stream"),
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

func (uploader *S3Uploader) DeleteBefore(ctx context.Context, cutoff time.Time) error {
	prefix := strings.TrimSuffix(uploader.opts.Prefix, "/") + "/"
	objects, err := uploader.List(ctx, prefix)
	if err != nil {
		return err
	}
	old := make([]s3types.ObjectIdentifier, 0, len(objects))
	for _, object := range objects {
		if object.LastModified.IsZero() ||
			(!isBackupObject(object.Key, prefix, true) && !isBackupObject(object.Key, prefix, false)) ||
			!object.LastModified.Before(cutoff) {
			continue
		}
		old = append(old, s3types.ObjectIdentifier{Key: aws.String(object.Key)})
	}
	for len(old) > 0 {
		batch := old
		if len(batch) > 1000 {
			batch = old[:1000]
		}
		old = old[len(batch):]
		if _, err := uploader.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(uploader.opts.Bucket),
			Delete: &s3types.Delete{Objects: batch, Quiet: aws.Bool(true)},
		}); err != nil {
			return fmt.Errorf("backup: delete old S3 objects: %w", err)
		}
	}
	return nil
}

// List returns objects under prefix. It is used by retention and by restore.
func (uploader *S3Uploader) List(ctx context.Context, prefix string) ([]RemoteObject, error) {
	paginator := s3.NewListObjectsV2Paginator(uploader.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(uploader.opts.Bucket), Prefix: aws.String(prefix),
	})
	objects := make([]RemoteObject, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("backup: list S3 objects: %w", err)
		}
		for _, object := range page.Contents {
			item := RemoteObject{Key: aws.ToString(object.Key), Size: aws.ToInt64(object.Size)}
			if object.LastModified != nil {
				item.LastModified = *object.LastModified
			}
			objects = append(objects, item)
		}
	}
	return objects, nil
}

// Download writes one S3 object to dest with mode 0600.
func (uploader *S3Uploader) Download(ctx context.Context, key, dest string) error {
	object, err := uploader.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(uploader.opts.Bucket), Key: aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("backup: download s3://%s/%s: %w", uploader.opts.Bucket, key, err)
	}
	defer func() { _ = object.Body.Close() }()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(dest), ".kouja-download-*")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, object.Body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, dest)
}

func isBackupObject(key, prefix string, perBackupFolder bool) bool {
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	relative := strings.TrimPrefix(key, prefix)
	if relative == "" {
		return false
	}
	if perBackupFolder {
		parts := strings.SplitN(relative, "/", 2)
		if len(parts) != 2 || parts[1] == "" {
			return false
		}
		_, err := time.Parse("2006Jan021504", parts[0])
		return err == nil
	}
	name := pathBase(relative)
	if !strings.HasPrefix(name, "kouja-") {
		return false
	}
	stamp := strings.TrimPrefix(name, "kouja-")
	if len(stamp) <= len(timestampLayout) {
		return false
	}
	stamp = stamp[:len(timestampLayout)]
	_, err := time.Parse(timestampLayout, stamp)
	return err == nil
}

func pathBase(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}
