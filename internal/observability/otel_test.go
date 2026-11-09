package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/state"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestOTelVolumeMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	statuses := []state.VolumeStatus{{
		Volume: state.Volume{
			Owner: "owner", VolumeID: "volume", LVName: "lv", VGName: "vg",
			RequestedSizeBytes: 100, ActualSizeBytes: 80, State: "exported",
		},
		Export: &state.Export{Protocol: "iscsi"},
		Observation: &state.Observation{
			LVPresent: true, ObservedSizeBytes: 90, ObservedAt: time.Now(),
		},
		ExportObservation: &state.ExportObservation{ActiveSessions: 2},
	}}
	reporter, err := newOTelWithReader(reader, OTelOptions{
		ServiceName: "kouja-test", ServiceVersion: "test-version", HostUUID: "host-uuid",
		StorageClass: func(string) string { return "gold" },
		Volumes:      func(context.Context) ([]state.VolumeStatus, error) { return statuses, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reporter.Shutdown(context.Background()) }()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	if len(collected.ScopeMetrics) == 0 || len(collected.ScopeMetrics[0].Metrics) == 0 {
		t.Fatal("no metrics collected")
	}
	metrics := collectGaugeMetrics(t, collected)
	if value := metrics["kouja.lv.requested_bytes"]; value != 100 {
		t.Fatalf("requested = %d", value)
	}
	if value := metrics["kouja.lv.actual_bytes"]; value != 80 {
		t.Fatalf("actual = %d", value)
	}
	if value := metrics["kouja.lv.observed_bytes"]; value != 90 {
		t.Fatalf("observed = %d", value)
	}
	if value := metrics["kouja.lv.present"]; value != 1 {
		t.Fatalf("present = %d", value)
	}
	if value := metrics["kouja.lv.exported"]; value != 1 {
		t.Fatalf("exported = %d", value)
	}
	if value := metrics["kouja.lv.active_sessions"]; value != 2 {
		t.Fatalf("sessions = %d", value)
	}
	if value := metrics["kouja.lv.info"]; value != 1 {
		t.Fatalf("info = %d", value)
	}
	attributes := firstGaugeAttributes(t, collected, "kouja.lv.requested_bytes")
	for key, want := range map[string]string{
		"kouja.owner": "owner", "kouja.volume_id": "volume", "kouja.lv_name": "lv",
		"kouja.vg_name": "vg", "kouja.storage_class": "gold", "kouja.state": "exported",
	} {
		if got, ok := attributes.Value(attribute.Key(key)); !ok || got.AsString() != want {
			t.Fatalf("attribute %s = %q/%v, want %q; all=%v", key, got.AsString(), ok, want, attributes.ToSlice())
		}
	}
}

func TestOTelVolumeWithoutObservation(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	reporter, err := newOTelWithReader(reader, OTelOptions{
		Volumes: func(context.Context) ([]state.VolumeStatus, error) {
			return []state.VolumeStatus{{Volume: state.Volume{Owner: "o", VolumeID: "v", State: "available"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reporter.Shutdown(context.Background()) }()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	metrics := collectGaugeMetrics(t, collected)
	if metrics["kouja.lv.present"] != 0 || metrics["kouja.lv.exported"] != 0 || metrics["kouja.lv.active_sessions"] != 0 {
		t.Fatalf("metrics = %#v", metrics)
	}
	if _, ok := metrics["kouja.lv.observed_bytes"]; ok {
		t.Fatal("observed bytes emitted without an observation")
	}
}

func TestOTelSourceErrorsAndDisabled(t *testing.T) {
	if reporter, err := NewOTel(context.Background(), OTelOptions{}); reporter != nil || err != nil {
		t.Fatalf("disabled NewOTel = %#v, %v", reporter, err)
	}
	if _, err := NewOTel(context.Background(), OTelOptions{Enabled: true}); err == nil {
		t.Fatal("missing volume source did not fail")
	}
	reader := sdkmetric.NewManualReader()
	reporter, err := newOTelWithReader(reader, OTelOptions{
		Volumes: func(context.Context) ([]state.VolumeStatus, error) {
			return nil, errors.New("source failed")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reporter.Shutdown(context.Background()) }()
	if err := reporter.collect(context.Background(), nil); err == nil {
		t.Fatal("source error was not returned")
	}
	if boolInt64(true) != 1 || boolInt64(false) != 0 {
		t.Fatal("boolInt64 is wrong")
	}
	if err := (*OTel)(nil).Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func collectGaugeMetrics(t *testing.T, collected metricdata.ResourceMetrics) map[string]int64 {
	t.Helper()
	result := make(map[string]int64)
	for _, scope := range collected.ScopeMetrics {
		for _, item := range scope.Metrics {
			gauge, ok := item.Data.(metricdata.Gauge[int64])
			if !ok || len(gauge.DataPoints) == 0 {
				continue
			}
			result[item.Name] = gauge.DataPoints[0].Value
		}
	}
	return result
}

func firstGaugeAttributes(t *testing.T, collected metricdata.ResourceMetrics, name string) attribute.Set {
	t.Helper()
	for _, scope := range collected.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name != name {
				continue
			}
			gauge, ok := item.Data.(metricdata.Gauge[int64])
			if ok && len(gauge.DataPoints) > 0 {
				return gauge.DataPoints[0].Attributes
			}
		}
	}
	t.Fatalf("metric %s not found", name)
	return attribute.NewSet()
}

func TestOTelResourceAndNoExportAttributes(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	reporter, err := newOTelWithReader(reader, OTelOptions{
		ServiceName: "svc", ServiceVersion: "v1", HostUUID: "host",
		Volumes: func(context.Context) ([]state.VolumeStatus, error) {
			return []state.VolumeStatus{{Volume: state.Volume{Owner: "o", VolumeID: "v", LVName: "lv", VGName: "vg", RequestedSizeBytes: 1, State: "available"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reporter.Shutdown(context.Background()) }()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	if collected.Resource == nil {
		t.Fatal("missing resource")
	}
	for key, want := range map[string]string{"service.name": "svc", "service.version": "v1", "service.instance.id": "host", "host.id": "host"} {
		found := false
		for _, value := range collected.Resource.Attributes() {
			if string(value.Key) == key && value.Value.AsString() == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("resource attribute %s=%q missing", key, want)
		}
	}
	metrics := collectGaugeMetrics(t, collected)
	if metrics["kouja.lv.requested_bytes"] != 1 || metrics["kouja.lv.exported"] != 0 || metrics["kouja.lv.present"] != 0 {
		t.Fatalf("metrics = %#v", metrics)
	}
	attributes := firstGaugeAttributes(t, collected, "kouja.lv.requested_bytes")
	if got, ok := attributes.Value(attribute.Key("kouja.export_protocol")); ok {
		t.Fatalf("unexpected export protocol %q", got.AsString())
	}
}

func TestNewOTelPullHandlerIncludesLegacyMetrics(t *testing.T) {
	legacy := New()
	legacy.Inc("kouja_legacy_total")
	legacy.Set("kouja_legacy_gauge", 2)
	reporter, err := NewOTel(context.Background(), OTelOptions{
		Enabled: true, ServiceName: "svc",
		Metrics: legacy,
		Volumes: func(context.Context) ([]state.VolumeStatus, error) {
			return []state.VolumeStatus{{Volume: state.Volume{
				Owner: "o", VolumeID: "v", LVName: "lv", VGName: "vg",
				RequestedSizeBytes: 10, ActualSizeBytes: 9, State: "available",
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reporter.Shutdown(context.Background()) }()
	recorder := httptest.NewRecorder()
	reporter.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, name := range []string{"kouja_legacy_total", "kouja_legacy_gauge", "kouja_lv_requested_bytes"} {
		if !strings.Contains(body, name) {
			t.Fatalf("metrics body missing %s: %s", name, body)
		}
	}
}

func TestMetricsSnapshot(t *testing.T) {
	metrics := New()
	metrics.Inc("counter")
	metrics.Set("gauge", 4)
	counters, gauges := metrics.Snapshot()
	if counters["counter"] != 1 || gauges["gauge"] != 4 {
		t.Fatalf("snapshot = %#v %#v", counters, gauges)
	}
	if nilCounters, nilGauges := (*Metrics)(nil).Snapshot(); nilCounters == nil || nilGauges == nil {
		t.Fatal("nil snapshot is not usable")
	}
}
