// Package telemetry sets up a service's logs. With an OTLP endpoint in the
// standard OTEL_EXPORTER_OTLP_* environment variables, they go to it as
// OpenTelemetry logs; without one, they go to stdout as text. Either way, a
// record written while handling a request carries the request's trace id and the
// attributes its handlers added, so its logs can be found together, also across
// services. Email addresses are replaced with <email> in every record.
//
// The ginlog and httplog packages log each request a gin or net/http server answers.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Shutdown sends the logs still buffered. Call it last, before the process exits.
type Shutdown func(context.Context) error

// Setup makes slog's default logger write the service's logs, at level and above
// (debug, info, warn or error; info when empty). It also starts a tracer
// provider that records and exports no spans: it only gives each request a trace
// id for its logs, and passes it on to the services the request calls.
func Setup(ctx context.Context, serviceName, level string) (Shutdown, error) {
	minimum, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	if err := checkProtocol(); err != nil {
		return nil, err
	}
	// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override the defaults
	serviceResource, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	// A partial resource, such as without the host name, still describes the service
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, fmt.Errorf("describing the service for its logs: %w", err)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "OpenTelemetry:", err)
	}

	// Created first, so a failure leaves OpenTelemetry's globals as they were
	var loggerProvider *sdklog.LoggerProvider
	if exportsLogs() {
		// OTLP over HTTP; OTEL_EXPORTER_OTLP_* set the endpoint, headers and timeouts
		exporter, err := otlploghttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("creating the OTLP log exporter: %w", err)
		}
		loggerProvider = sdklog.NewLoggerProvider(
			sdklog.WithResource(serviceResource),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		)
	}

	// The sampler drops every span, so none is recorded, but each still gets ids
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(serviceResource),
		sdktrace.WithSampler(sdktrace.NeverSample()),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	// The SDK's own errors, such as a failed export, can't go through the logs
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		fmt.Fprintln(os.Stderr, "OpenTelemetry:", err)
	}))
	shutdowns := []Shutdown{tracerProvider.Shutdown}

	var handler slog.Handler
	if loggerProvider != nil {
		shutdowns = append(shutdowns, loggerProvider.Shutdown)
		// The bridge adds the trace and span ids to each record itself
		handler = &contextHandler{
			next:    otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(loggerProvider)),
			minimum: minimum,
		}
	} else {
		handler = TextHandler(os.Stdout, minimum)
	}
	slog.SetDefault(slog.New(handler))

	return func(ctx context.Context) error {
		var errs []error
		for i := len(shutdowns) - 1; i >= 0; i-- {
			errs = append(errs, shutdowns[i](ctx))
		}
		return errors.Join(errs...)
	}, nil
}

// exportsLogs reports whether an OTLP endpoint for logs is configured
func exportsLogs() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

// checkProtocol refuses an OTLP protocol other than HTTP with protobuf, the only
// one Setup sends logs with, so they aren't lost to a gRPC endpoint
func checkProtocol() error {
	for _, name := range []string{"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL"} {
		if protocol := os.Getenv(name); protocol != "" {
			if protocol != "http/protobuf" {
				return fmt.Errorf("%s is %q; the logs are sent with http/protobuf only", name, protocol)
			}
			return nil
		}
	}
	return nil
}

// ParseLevel reads a LOG_LEVEL value: debug, info, warn (or warning) or error, in
// any case. Empty means info.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q; use debug, info, warn or error", level)
}
