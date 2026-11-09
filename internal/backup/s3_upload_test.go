package backup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS3UploaderPutObject(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	var method, path, encryption string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method = request.Method
		path = request.URL.Path
		encryption = request.Header.Get("X-Amz-Server-Side-Encryption")
		response.Header().Set("ETag", `"etag"`)
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	uploader, err := NewS3Uploader(context.Background(), S3Options{
		Bucket: "bucket", Prefix: "prefix/", Region: "us-east-1", Endpoint: server.URL,
		ForcePathStyle: true, ServerSideEncryption: "AES256",
	})
	if err != nil {
		t.Fatal(err)
	}
	pathOnDisk := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(pathOnDisk, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := uploader.Upload(context.Background(), pathOnDisk, "prefix/archive.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/bucket/prefix/archive.tar.gz" || !strings.EqualFold(encryption, "AES256") {
		t.Fatalf("request = %s %s encryption=%q", method, path, encryption)
	}
}
