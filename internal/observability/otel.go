package observability

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/voidstarit/kouja/internal/state"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// OTelOptions configures the optional OTLP metrics exporter. Per-volume
// metrics are collected asynchronously from Volumes when the SDK exports.
type OTelOptions struct {
	Enabled        bool
	Endpoint       string
	Insecure       bool
	Headers        map[string]string
	Interval       time.Duration
	ServiceName    string
	ServiceVersion string
	HostUUID       string
	StorageClass   func(vg string) string
	Volumes        func(context.Context) ([]state.VolumeStatus, error)
	Logger         *slog.Logger
}

type otelInstruments struct {
	requestedBytes metric.Int64ObservableGauge
	actualBytes    metric.Int64ObservableGauge
	observedBytes  metric.Int64ObservableGauge
	present        metric.Int64ObservableGauge
	exported       metric.Int64ObservableGauge
	activeSessions metric.Int64ObservableGauge
	info           metric.Int64ObservableGauge
}

// OTel owns the OpenTelemetry meter provider and its exporter lifecycle.
type OTel struct {
	provider    *sdkmetric.MeterProvider
	opts        OTelOptions
	instruments *otelInstruments
}

// NewOTel creates an OTLP/gRPC metrics pipeline. It returns nil when disabled.
func NewOTel(ctx context.Context, opts OTelOptions) (*OTel, error) {
	if !opts.Enabled {
		return nil, nil
	}
	if opts.Volumes == nil {
		return nil, fmt.Errorf("observability: volume source is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.ServiceName == "" {
		opts.ServiceName = "kouja-agent"
	}
	exporterOptions := make([]otlpmetricgrpc.Option, 0, 3)
	if opts.Endpoint != "" {
		exporterOptions = append(exporterOptions, otlpmetricgrpc.WithEndpoint(opts.Endpoint))
	}
	if opts.Insecure {
		exporterOptions = append(exporterOptions, otlpmetricgrpc.WithInsecure())
	}
	if len(opts.Headers) > 0 {
		exporterOptions = append(exporterOptions, otlpmetricgrpc.WithHeaders(opts.Headers))
	}
	exporterCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	exporter, err := otlpmetricgrpc.New(exporterCtx, exporterOptions...)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("observability: create OTLP exporter: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(exporter,
		sdkmetric.WithInterval(opts.Interval),
		sdkmetric.WithTimeout(10*time.Second),
	)
	provider := newMeterProvider(reader, opts)
	reporter, err := newOTelWithProvider(provider, opts)
	if err != nil {
		_ = provider.Shutdown(context.Background())
		return nil, err
	}
	otel.SetMeterProvider(provider)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger := opts.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("OpenTelemetry error", "error", err)
	}))
	return reporter, nil
}

func newMeterProvider(reader sdkmetric.Reader, opts OTelOptions) *sdkmetric.MeterProvider {
	attributes := []attribute.KeyValue{
		attribute.String("service.name", opts.ServiceName),
	}
	if opts.ServiceVersion != "" {
		attributes = append(attributes, attribute.String("service.version", opts.ServiceVersion))
	}
	if opts.HostUUID != "" {
		attributes = append(attributes,
			attribute.String("service.instance.id", opts.HostUUID),
			attribute.String("host.id", opts.HostUUID),
		)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(resource.NewSchemaless(attributes...)),
	)
}

func newOTelWithProvider(provider *sdkmetric.MeterProvider, opts OTelOptions) (*OTel, error) {
	meter := provider.Meter("github.com/voidstarit/kouja", metric.WithInstrumentationVersion(opts.ServiceVersion))
	requested, err := meter.Int64ObservableGauge("kouja.lv.requested_bytes",
		metric.WithDescription("Requested logical volume size in bytes."), metric.WithUnit("By"))
	if err != nil {
		return nil, err
	}
	actual, err := meter.Int64ObservableGauge("kouja.lv.actual_bytes",
		metric.WithDescription("Actual logical volume size in bytes."), metric.WithUnit("By"))
	if err != nil {
		return nil, err
	}
	observed, err := meter.Int64ObservableGauge("kouja.lv.observed_bytes",
		metric.WithDescription("Observed logical volume size in bytes."), metric.WithUnit("By"))
	if err != nil {
		return nil, err
	}
	present, err := meter.Int64ObservableGauge("kouja.lv.present",
		metric.WithDescription("Whether the logical volume is present on the host (1) or not (0)."))
	if err != nil {
		return nil, err
	}
	exported, err := meter.Int64ObservableGauge("kouja.lv.exported",
		metric.WithDescription("Whether the logical volume has an export (1) or not (0)."))
	if err != nil {
		return nil, err
	}
	active, err := meter.Int64ObservableGauge("kouja.lv.active_sessions",
		metric.WithDescription("Observed active export sessions for the logical volume."))
	if err != nil {
		return nil, err
	}
	info, err := meter.Int64ObservableGauge("kouja.lv.info",
		metric.WithDescription("Logical volume identity and state information; the value is always 1."))
	if err != nil {
		return nil, err
	}
	reporter := &OTel{
		provider: provider, opts: opts,
		instruments: &otelInstruments{
			requestedBytes: requested, actualBytes: actual, observedBytes: observed,
			present: present, exported: exported, activeSessions: active, info: info,
		},
	}
	if _, err := meter.RegisterCallback(reporter.collect, requested, actual, observed, present, exported, active, info); err != nil {
		return nil, fmt.Errorf("observability: register OTel callback: %w", err)
	}
	return reporter, nil
}

// newOTelWithReader is used by tests and by alternate meter providers.
func newOTelWithReader(reader sdkmetric.Reader, opts OTelOptions) (*OTel, error) {
	provider := newMeterProvider(reader, opts)
	reporter, err := newOTelWithProvider(provider, opts)
	if err != nil {
		_ = provider.Shutdown(context.Background())
		return nil, err
	}
	return reporter, nil
}

func (reporter *OTel) collect(ctx context.Context, observer metric.Observer) error {
	if reporter == nil || reporter.opts.Volumes == nil {
		return nil
	}
	statuses, err := reporter.opts.Volumes(ctx)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		attributes := reporter.attributes(status)
		options := metric.WithAttributes(attributes...)
		observer.ObserveInt64(reporter.instruments.requestedBytes, status.RequestedSizeBytes, options)
		observer.ObserveInt64(reporter.instruments.actualBytes, status.ActualSizeBytes, options)
		if status.Observation != nil {
			observer.ObserveInt64(reporter.instruments.observedBytes, status.Observation.ObservedSizeBytes, options)
			observer.ObserveInt64(reporter.instruments.present, boolInt64(status.Observation.LVPresent), options)
		} else {
			observer.ObserveInt64(reporter.instruments.present, 0, options)
		}
		observer.ObserveInt64(reporter.instruments.exported, boolInt64(status.Export != nil), options)
		var sessions int64
		if status.ExportObservation != nil {
			sessions = int64(status.ExportObservation.ActiveSessions)
		}
		observer.ObserveInt64(reporter.instruments.activeSessions, sessions, options)
		observer.ObserveInt64(reporter.instruments.info, 1, options)
	}
	return nil
}

func (reporter *OTel) attributes(status state.VolumeStatus) []attribute.KeyValue {
	storageClass := status.VGName
	if reporter.opts.StorageClass != nil {
		if value := reporter.opts.StorageClass(status.VGName); value != "" {
			storageClass = value
		}
	}
	attributes := []attribute.KeyValue{
		attribute.String("kouja.owner", status.Owner),
		attribute.String("kouja.volume_id", status.VolumeID),
		attribute.String("kouja.lv_name", status.LVName),
		attribute.String("kouja.vg_name", status.VGName),
		attribute.String("kouja.storage_class", storageClass),
		attribute.String("kouja.state", status.State),
	}
	if status.Export != nil {
		attributes = append(attributes, attribute.String("kouja.export_protocol", status.Export.Protocol))
	}
	return attributes
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// Shutdown flushes and stops the metrics reader and exporter.
func (reporter *OTel) Shutdown(ctx context.Context) error {
	if reporter == nil || reporter.provider == nil {
		return nil
	}
	return reporter.provider.Shutdown(ctx)
}
