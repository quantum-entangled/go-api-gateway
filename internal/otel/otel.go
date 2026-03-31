package otel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// SDK holds OTel providers needed to create instruments and loggers.
// Call Shutdown on application exit to flush buffered telemetry data.
type SDK struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
}

// Setup initializes OTel with OTLP HTTP exporters for traces, metrics, and logs.
// It registers global TracerProvider, MeterProvider, and TextMapPropagator so
// that library code (e.g. proxy.injectTraceContext) can use otel.GetTextMapPropagator().
func Setup(ctx context.Context, endpoint string) (*SDK, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("go-api-gateway"),
		),
	)
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracehttp.New(
		ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
	)

	metricExp, err := otlpmetrichttp.New(
		ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithInsecure(),
	)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(10*time.Second),
		)),
		sdkmetric.WithResource(res),
	)

	logExp, err := otlploghttp.New(
		ctx,
		otlploghttp.WithEndpoint(endpoint),
		otlploghttp.WithInsecure(),
	)
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, err
	}

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return &SDK{
		TracerProvider: tp,
		MeterProvider:  mp,
		LoggerProvider: lp,
	}, nil
}

// Shutdown flushes and shuts down all providers.
func (s *SDK) Shutdown(ctx context.Context) error {
	return errors.Join(
		s.TracerProvider.Shutdown(ctx),
		s.MeterProvider.Shutdown(ctx),
		s.LoggerProvider.Shutdown(ctx),
	)
}

// NewLogger creates a slog.Logger that writes JSON to w and also sends log
// records to the OTel LoggerProvider for export to Loki via OTLP.
func (s *SDK) NewLogger(w io.Writer) *slog.Logger {
	jsonHandler := slog.NewJSONHandler(w, nil)
	otelHandler := otelslog.NewHandler(
		"go-api-gateway",
		otelslog.WithLoggerProvider(s.LoggerProvider),
	)
	return slog.New(fanoutHandler{handlers: []slog.Handler{jsonHandler, otelHandler}})
}
