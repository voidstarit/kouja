package backup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newS3TestUploader(t *testing.T, endpoint string, kms bool) *S3Uploader {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	uploader, err := NewS3Uploader(context.Background(), S3Options{
		Bucket: "bucket", Prefix: "prefix/", Region: "us-east-1", Endpoint: endpoint,
		ForcePathStyle: true, ServerSideEncryption: func() string {
			if kms {
				return "aws:kms"
			}
			return ""
		}(), KMSKeyID: "alias/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return uploader
}

func TestS3UploaderErrorBranches(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	if _, err := NewS3Uploader(context.Background(), S3Options{}); err == nil {
		t.Fatal("empty bucket did not fail")
	}
	var kmsHeader, kmsKey string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			kmsHeader = request.Header.Get("X-Amz-Server-Side-Encryption")
			kmsKey = request.Header.Get("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id")
			response.Header().Set("ETag", `"e"`)
			response.WriteHeader(http.StatusOK)
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	uploader := newS3TestUploader(t, server.URL, true)
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := uploader.Upload(context.Background(), path, "prefix/file"); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(kmsHeader, "aws:kms") || kmsKey != "alias/test" {
		t.Fatalf("KMS headers = %q %q", kmsHeader, kmsKey)
	}
	if err := uploader.Upload(context.Background(), filepath.Join(t.TempDir(), "missing"), "prefix/file"); err == nil {
		t.Fatal("missing upload file did not fail")
	}
	errorServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer errorServer.Close()
	errorUploader := newS3TestUploader(t, errorServer.URL, false)
	if err := errorUploader.Upload(context.Background(), path, "prefix/file"); err == nil {
		t.Fatal("error upload did not fail")
	}
	if _, err := errorUploader.List(context.Background(), "prefix/"); err == nil {
		t.Fatal("error list did not fail")
	}
	if err := errorUploader.DeleteBefore(context.Background(), time.Now()); err == nil {
		t.Fatal("error retention list did not fail")
	}
	if err := errorUploader.Download(context.Background(), "prefix/file", filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("error download did not fail")
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	okServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, "content")
	}))
	defer okServer.Close()
	okUploader := newS3TestUploader(t, okServer.URL, false)
	if err := okUploader.Download(context.Background(), "prefix/file", filepath.Join(blocker, "out")); err == nil {
		t.Fatal("blocked download destination did not fail")
	}
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil { //nolint:gosec // deliberate permission test
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnly, 0o700) }() //nolint:gosec // restore test directory
	if err := okUploader.Download(context.Background(), "prefix/file", filepath.Join(readOnly, "out")); err == nil {
		t.Fatal("read-only download destination did not fail")
	}
	if pathBase("prefix/file") != "file" || pathBase("file") != "file" {
		t.Fatal("pathBase result is wrong")
	}
}

func TestS3UploaderDeleteErrorAfterList(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2":
			response.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(response, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>bucket</Name><Prefix>prefix/</Prefix><KeyCount>1</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated><Contents><Key>prefix/2026Nov091200/state.db</Key><LastModified>2026-11-01T00:00:00.000Z</LastModified><Size>4</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents></ListBucketResult>`)
		case request.Method == http.MethodPost && request.URL.Query().Has("delete"):
			response.WriteHeader(http.StatusInternalServerError)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	uploader := newS3TestUploader(t, server.URL, false)
	if err := uploader.DeleteBefore(context.Background(), time.Now()); err == nil {
		t.Fatal("delete error did not fail")
	}
}
