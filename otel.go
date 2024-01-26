package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// setupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func setupOTelSDK(ctx context.Context, serviceName, serviceVersion string) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	// Set up resource.
	res, err := newResource(serviceName, serviceVersion)
	if err != nil {
		handleErr(err)
		return
	}

	// Set up propagator.
	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	tracerProvider, err := newTraceProvider(res)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Set up meter provider.
	meterProvider, err := newMeterProvider(res)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	return
}

func newResource(serviceName, serviceVersion string) (*resource.Resource, error) {
	return resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		))
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

var collectorEndpoint string

func initGrpcProcessor() (trace.SpanProcessor, error) {
	if collectorEndpoint == "" {
		return nil, nil
	}
	ctx := context.Background()

	// If the OpenTelemetry Collector is running on a local cluster (minikube or
	// microk8s), it should be accessible through the NodePort service at the
	// `localhost:30080` endpoint. Otherwise, replace `localhost` with the
	// endpoint of your cluster. If you run the app inside k8s, then you can
	// probably connect directly to the service through dns.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, collectorEndpoint,
		// Note the use of insecure transport here. TLS is recommended in production.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection to collector: %w", err)
	}

	// Set up a trace exporter
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	return trace.NewBatchSpanProcessor(traceExporter), nil
}

func newTraceProvider(res *resource.Resource) (*trace.TracerProvider, error) {
	traceExporter, err := stdouttrace.New(
		stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	gp, err := initGrpcProcessor()
	if err != nil {
		return nil, err
	}

	opts := []trace.TracerProviderOption{
		trace.WithBatcher(traceExporter,
			// Default is 5s. Set to 1s for demonstrative purposes.
			trace.WithBatchTimeout(time.Second)),
		trace.WithResource(res),
	}
	if gp != nil {
		opts = append(opts, trace.WithSpanProcessor(gp))
	}

	traceProvider := trace.NewTracerProvider(opts...)
	return traceProvider, nil
}

func newMeterProvider(res *resource.Resource) (*metric.MeterProvider, error) {
	metricExporter, err := stdoutmetric.New()
	if err != nil {
		return nil, err
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter,
			// Default is 1m. Set to 3s for demonstrative purposes.
			metric.WithInterval(3*time.Second))),
	)
	return meterProvider, nil
}
