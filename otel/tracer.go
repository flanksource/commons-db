package otel

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/commons/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
)

type Tracer struct {
	ServiceName    string
	CollectorURL   string
	Insecure       bool
	Samplers       map[string]sdktrace.Sampler
	SpanProcessors []sdktrace.SpanProcessor
	Exporter       sdktrace.SpanExporter
}

func (tracer Tracer) Sample(name string, perc float64) Tracer {
	tracer.Samplers[name] = NewCounterSampler(perc)
	return tracer
}

// Init installs the global tracer provider. The returned shutdown flushes
// buffered spans, logging a failure to do so.
func (tracer Tracer) Init() (func(), error) {
	shutdown, err := tracer.Start(context.Background())
	if err != nil {
		return nil, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			logger.Errorf("%s", err)
		}
	}, nil
}

// Start installs the global tracer provider and returns a shutdown that
// flushes buffered spans before closing the exporter.
func (tracer Tracer) Start(ctx context.Context) (func(context.Context) error, error) {
	return tracer.init(func() (sdktrace.SpanExporter, error) {
		return tracer.newExporter(ctx)
	}, func() (*resource.Resource, error) {
		return resource.New(ctx, resource.WithAttributes(attribute.String("service.name", tracer.ServiceName)))
	})
}

func (tracer Tracer) newExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	if tracer.Exporter != nil {
		return tracer.Exporter, nil
	}
	client, err := tracer.client()
	if err != nil {
		return nil, err
	}
	return otlptrace.New(ctx, client)
}

func (tracer Tracer) init(newExporter func() (sdktrace.SpanExporter, error), newResource func() (*resource.Resource, error)) (func(context.Context) error, error) {
	exporter, err := newExporter()
	if err != nil {
		return nil, fmt.Errorf("create opentelemetry exporter: %w", err)
	}
	resources, err := newResource()
	if err != nil {
		if shutdownErr := exporter.Shutdown(context.Background()); shutdownErr != nil {
			return nil, fmt.Errorf("create opentelemetry resource: %w; shutdown exporter: %w", err, shutdownErr)
		}
		return nil, fmt.Errorf("create opentelemetry resource: %w", err)
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(NewCustomSampler(sdktrace.AlwaysSample(), tracer.Samplers)),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resources),
	}
	for _, processor := range tracer.SpanProcessors {
		options = append(options, sdktrace.WithSpanProcessor(processor))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return provider.Shutdown, nil
}

// client picks OTLP/HTTP for an http(s):// collector URL, whose scheme decides
// TLS, and OTLP/gRPC for a bare host:port, which uses TLS unless Insecure. A
// URL without a path is a base URL, as OTEL_EXPORTER_OTLP_ENDPOINT is, so
// spans go to its /v1/traces.
func (tracer Tracer) client() (otlptrace.Client, error) {
	if strings.HasPrefix(tracer.CollectorURL, "http") {
		u, err := url.Parse(tracer.CollectorURL)
		if err != nil {
			return nil, fmt.Errorf("invalid opentelemetry collector url %q: %w", tracer.CollectorURL, err)
		}
		if u.Path == "" || u.Path == "/" {
			u.Path = "/v1/traces"
		}
		return otlptracehttp.NewClient(
			otlptracehttp.WithEndpointURL(u.String()),
			otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
		), nil
	}

	secureOption := otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
	if tracer.Insecure {
		secureOption = otlptracegrpc.WithInsecure()
	}
	return otlptracegrpc.NewClient(secureOption, otlptracegrpc.WithEndpoint(tracer.CollectorURL)), nil
}
