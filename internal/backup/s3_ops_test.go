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

const s3ListResponse = `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>bucket</Name><Prefix>prefix/</Prefix><KeyCount>3</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>
  <Contents><Key>prefix/2026Nov091200/state.db</Key><LastModified>2026-11-01T00:00:00.000Z</LastModified><Size>4</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents>
  <Contents><Key>prefix/2026Nov091200/restore.json</Key><LastModified>2026-11-01T00:00:00.000Z</LastModified><Size>2</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents>
  <Contents><Key>prefix/2026Nov091300/state.db</Key><LastModified>2026-11-08T00:00:00.000Z</LastModified><Size>4</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents>
  <Contents><Key>prefix/unrelated.txt</Key><LastModified>2026-11-01T00:00:00.000Z</LastModified><Size>1</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents>
</ListBucketResult>`

func TestS3UploaderListDownloadAndDeleteBefore(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	var deleted string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2":
			response.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(response, s3ListResponse)
		case request.Method == http.MethodGet:
			_, _ = io.WriteString(response, "object-body")
		case request.Method == http.MethodPost && request.URL.Query().Has("delete"):
			body, _ := io.ReadAll(request.Body)
			deleted = string(body)
			response.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(response, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	uploader, err := NewS3Uploader(context.Background(), S3Options{
		Bucket: "bucket", Prefix: "prefix/", Region: "us-east-1", Endpoint: server.URL,
		ForcePathStyle: true, PerBackupFolder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := uploader.List(context.Background(), "prefix/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 4 || objects[0].Key != "prefix/2026Nov091200/state.db" || objects[0].Size != 4 {
		t.Fatalf("objects = %#v", objects)
	}
	destination := filepath.Join(t.TempDir(), "downloaded")
	if err := uploader.Download(context.Background(), "prefix/2026Nov091200/state.db", destination); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "object-body" { //nolint:gosec // test file
		t.Fatalf("download = %q, %v", data, err)
	}
	cutoff := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	if err := uploader.DeleteBefore(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"2026Nov091200/state.db", "2026Nov091200/restore.json"} {
		if !strings.Contains(deleted, key) {
			t.Fatalf("delete body missing %s: %s", key, deleted)
		}
	}
	for _, key := range []string{"2026Nov091300/state.db", "unrelated.txt"} {
		if strings.Contains(deleted, key) {
			t.Fatalf("delete body contains %s: %s", key, deleted)
		}
	}
}
