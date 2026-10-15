package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// HealthPath is the health check's path, which the middleware don't trace or log
const HealthPath = "/health"

// RequestLevels are the levels requests are logged at, by how they ended. Failed
// requests (5xx) and those whose handler panicked are always logged as errors.
type RequestLevels struct {
	// Succeeded is for 1xx to 3xx
	Succeeded slog.Level
	// Refused is for 4xx
	Refused slog.Level
}

// Request is how a request ended, for its log line
type Request struct {
	Method string
	// Path is the path as it may be logged, such as with an email address replaced
	Path string
	// Route is the route that matched, such as /v1/users/:id, when there is one
	Route    string
	Status   int
	Duration time.Duration
	Panicked bool
}

// LogRequest writes the line for an answered request, at the level for how it
// ended, with OpenTelemetry's names for its attributes
func LogRequest(ctx context.Context, levels RequestLevels, request Request) {
	level := levels.Succeeded
	switch {
	case request.Status >= http.StatusInternalServerError || request.Panicked:
		level = slog.LevelError
	case request.Status >= http.StatusBadRequest:
		level = levels.Refused
	}
	attributes := []any{
		"http.request.method", request.Method,
		"url.path", request.Path,
		"http.response.status_code", request.Status,
		"duration_ms", request.Duration.Milliseconds(),
	}
	if request.Route != "" {
		attributes = append(attributes, "http.route", request.Route)
	}
	slog.Log(ctx, level, "Answered a request", attributes...)
}

// LogPanic logs what a request's handler panicked with, and where
func LogPanic(ctx context.Context, recovered any) {
	slog.ErrorContext(ctx, "A request's handler panicked",
		"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
}
