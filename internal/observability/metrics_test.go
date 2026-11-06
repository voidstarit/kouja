package observability

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandler(t *testing.T) {
	metrics := New()
	metrics.Inc("test_counter")
	metrics.Set("test_gauge", 3)
	request := httptest.NewRequest("GET", "/metrics", nil)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "test_counter") {
		t.Fatalf("metrics response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestMetricsRenderAndNilReceiver(t *testing.T) {
	metrics := New()
	metrics.Inc("counter")
	metrics.Set("gauge", 2)
	rendered := metrics.Render()
	if !strings.Contains(rendered, "counter 1") || !strings.Contains(rendered, "gauge 2") {
		t.Fatalf("render = %q", rendered)
	}
	var nilMetrics *Metrics
	nilMetrics.Inc("ignored")
	nilMetrics.Set("ignored", 1)
}
