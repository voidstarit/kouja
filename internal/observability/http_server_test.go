package observability

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestHTTPServerServesMetricsAndShutsDown(t *testing.T) {
	server, err := NewHTTPServer("127.0.0.1:0", http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "metrics")
	}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve()
	}()
	response, err := http.Get("http://" + server.Addr().String()) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-done
	if _, err := NewHTTPServer("bad address", http.NotFoundHandler()); err == nil {
		t.Fatal("invalid listen address did not fail")
	}
}
