package otel

import (
	"context"
	"crypto/tls"
	"fmt"
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

func (tracer Tracer) Init() (func(), error) {
	return tracer.init(tracer.newExporter, func() (*resource.Resource, error) {
		return resource.New(context.Background(), resource.WithAttributes(attribute.String("service.name", tracer.ServiceName)))
	})
}

func (tracer Tracer) newExporter() (sdktrace.SpanExporter, error) {
	if tracer.Exporter != nil {
		return tracer.Exporter, nil
	}
	var client otlptrace.Client
	if strings.HasPrefix(tracer.CollectorURL, "http") {
		client = otlptracehttp.NewClient(
			otlptracehttp.WithInsecure(),
			otlptracehttp.WithEndpoint(strings.ReplaceAll(tracer.CollectorURL, "https://", "")),
			otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
			otlptracehttp.WithTLSClientConfig(&tls.Config{}))
	} else {
		var secureOption otlptracegrpc.Option

		if !tracer.Insecure {
			secureOption = otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
		} else {
			secureOption = otlptracegrpc.WithInsecure()
		}

		client = otlptracegrpc.NewClient(
			secureOption,
			otlptracegrpc.WithEndpoint(tracer.CollectorURL),
		)
	}

	return otlptrace.New(context.Background(), client)
}

func (tracer Tracer) init(newExporter func() (sdktrace.SpanExporter, error), newResource func() (*resource.Resource, error)) (func(), error) {
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

	// Register the TraceContext propagator globally.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
		defer cancel()
		err := provider.Shutdown(ctx)
		if err != nil {
			logger.Errorf("%s", err)
		}
	}, nil
}
