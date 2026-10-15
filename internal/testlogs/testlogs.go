// Package testlogs lets the tests read what is logged
package testlogs

import (
	"bytes"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Update-Prompt/go-telemetry"
)

// Use makes slog's default logger write text to the returned buffer, as Setup
// does without an OTLP endpoint, and traces with the SDK's tracer provider,
// until the test ends
func Use(t *testing.T, minimum slog.Level) *bytes.Buffer {
	t.Helper()
	logs := &bytes.Buffer{}
	previousLogger, previousProvider := slog.Default(), otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	slog.SetDefault(slog.New(telemetry.TextHandler(logs, minimum)))
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	return logs
}
