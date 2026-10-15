package telemetry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/Update-Prompt/go-telemetry"
	"github.com/Update-Prompt/go-telemetry/internal/testlogs"
)

// Given each level name LOG_LEVEL accepts, in any case and with spaces, and an unknown one
// When it is parsed
// Then each known name gives its level and the unknown one is refused
func TestParseLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"DEBUG":   slog.LevelDebug,
		" warn ":  slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for value, want := range tests {
		got, err := telemetry.ParseLevel(value)
		if err != nil || got != want {
			t.Errorf("telemetry.ParseLevel(%q): expected %v, got %v and %v", value, want, got, err)
		}
	}
	if _, err := telemetry.ParseLevel("verbose"); err == nil {
		t.Error("Expected an unknown level to be refused")
	}
}

func TestTextLogsCarryTheRequestAndTraceIDs(t *testing.T) {
	// Given a request's context with a request id, a user id and a span
	logs := testlogs.Use(t, slog.LevelInfo)
	ctx := telemetry.WithLogAttrs(context.Background(), slog.String("request_id", "req_1"),
		slog.String("client", "ada@example.com"))
	ctx = telemetry.WithLogAttrs(ctx, slog.String("user_id", "user_1"))
	ctx, span := otel.Tracer("test").Start(ctx, "request")
	defer span.End()

	// When a record is written with it, through a logger with attributes
	slog.With("worker", "w1").InfoContext(ctx, "Stored a batch")

	// Then it carries the ids and the attributes
	line := logs.String()
	for _, want := range []string{
		"msg=\"Stored a batch\"", "worker=w1", "request_id=req_1", "client=<email>", "user_id=user_1",
		"trace_id=" + span.SpanContext().TraceID().String(), "span_id=" + span.SpanContext().SpanID().String(),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("Expected %s in %q", want, line)
		}
	}
}

func TestRequestAttrsReachLaterRecords(t *testing.T) {
	// Given a request's context
	logs := testlogs.Use(t, slog.LevelInfo)
	ctx := telemetry.WithRequest(context.Background())
	slog.InfoContext(ctx, "Started")

	// When a handler adds the user's id, and a record is written after
	telemetry.AddLogAttrs(ctx, slog.String("user_id", "user_1"))
	slog.InfoContext(ctx, "Answered")

	// Then only the later record carries it
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "user_id") ||
		!strings.Contains(lines[1], "user_id=user_1") {
		t.Errorf("Expected the user id on the second line only, got %q", logs.String())
	}
}

// Given a context that isn't a request's
// When attributes are added to it
// Then nothing happens and records carry none
func TestAddLogAttrsOutsideARequest(t *testing.T) {
	logs := testlogs.Use(t, slog.LevelInfo)

	telemetry.AddLogAttrs(context.Background(), slog.String("user_id", "user_1"))
	slog.InfoContext(context.Background(), "Started")

	if strings.Contains(logs.String(), "user_id") {
		t.Errorf("Expected no user id, got %q", logs.String())
	}
}

func TestLogsHoldNoEmailAddresses(t *testing.T) {
	// Given errors and values quoting email addresses
	logs := testlogs.Use(t, slog.LevelInfo)
	smtpError := errors.New("550 5.1.1 <ada@example.com>: Recipient address rejected")

	// When they are logged, directly, in a group and through a logger's attributes
	slog.With("to", "Ada.Lovelace+otp@mail.example.co.uk").
		Error("Failed to send to grace@example.org",
			"error", smtpError, "detail", slog.GroupValue(slog.String("from", "noreply@updateprompt.com")),
			"database", "postgres://usage@localhost:5432/usage", "port", 8084)

	// Then no address is left, and the other values are as they were
	line := logs.String()
	for _, address := range []string{"ada@", "Lovelace", "grace@", "noreply@"} {
		if strings.Contains(line, address) {
			t.Errorf("Expected no %s in %q", address, line)
		}
	}
	for _, want := range []string{"msg=\"Failed to send to <email>\"", "to=<email>", "detail.from=<email>",
		"error=\"550 5.1.1 <<email>>: Recipient address rejected\"",
		"database=postgres://usage@localhost:5432/usage", "port=8084"} {
		if !strings.Contains(line, want) {
			t.Errorf("Expected %s in %q", want, line)
		}
	}
}

func TestLogsHoldNoEmailAddressesInOtherValues(t *testing.T) {
	// Given values that aren't strings but hold email addresses
	logs := testlogs.Use(t, slog.LevelInfo)

	// When they are logged
	slog.Info("Sent", "user", userValue{email: "ada@example.com"},
		"to", stringer{text: "grace@example.org"}, "cc", []string{"alan@example.net"}, "tries", []int{1, 2})

	// Then no address is left, and the other values are as they were
	line := logs.String()
	for _, address := range []string{"ada@", "grace@", "alan@"} {
		if strings.Contains(line, address) {
			t.Errorf("Expected no %s in %q", address, line)
		}
	}
	if !strings.Contains(line, "tries=\"[1 2]\"") {
		t.Errorf("Expected the tries as they were in %q", line)
	}
}

// Given text logs
// When a record is written outside a request
// Then it carries no request or trace id
func TestTextLogsWithoutARequest(t *testing.T) {
	logs := testlogs.Use(t, slog.LevelInfo)

	slog.Info("Starting the usage worker")

	if line := logs.String(); strings.Contains(line, "request_id") ||
		strings.Contains(line, "trace_id") {
		t.Errorf("Expected no ids outside a request, got %q", line)
	}
}

// Given text logs at the warn level
// When an info and a warning are written
// Then only the warning is kept
func TestLogsBelowTheLevelAreDropped(t *testing.T) {
	logs := testlogs.Use(t, slog.LevelWarn)

	slog.Info("Starting the usage worker")
	slog.Warn("Created the lost events' consumer group again")

	if line := logs.String(); strings.Contains(line, "Starting") ||
		!strings.Contains(line, "Created") {
		t.Errorf("Expected only the warning, got %q", line)
	}
}

func TestSetupExportsOverOTLP(t *testing.T) {
	// Given an OTLP endpoint
	var exports atomic.Int32
	collector := httptest.NewServer(
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPost && request.URL.Path == "/v1/logs" {
				_, _ = io.Copy(io.Discard, request.Body)
				exports.Add(1)
			}
			writer.WriteHeader(http.StatusOK)
		}),
	)
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	previousLogger, previousProvider := slog.Default(), otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	defer func() {
		slog.SetDefault(previousLogger)
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	}()

	// When the service logs and shuts down
	shutdown, err := telemetry.Setup(context.Background(), "test-service", "info")
	if err != nil {
		t.Fatalf("Expected the setup to succeed, got %v", err)
	}
	slog.Error("Failed to read the queue", "error", "connection refused")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("Expected the shutdown to succeed, got %v", err)
	}

	// Then the record went to the endpoint
	if exports.Load() == 0 {
		t.Error("Expected the logs to be exported to the OTLP endpoint")
	}
}

// Given an OTLP endpoint and the gRPC protocol
// When the logs are set up
// Then the setup fails, since logs are sent over HTTP only
func TestSetupRefusesTheGRPCProtocol(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	if _, err := telemetry.Setup(context.Background(), "test-service", "info"); err == nil {
		t.Error("Expected the gRPC protocol to be refused")
	}
}

// Given an unknown level
// When the logs are set up
// Then the setup fails
func TestSetupRefusesAnUnknownLevel(t *testing.T) {
	if _, err := telemetry.Setup(context.Background(), "test-service", "verbose"); err == nil {
		t.Error("Expected an unknown level to be refused")
	}
}

// Helpers

// userValue logs as a group with the user's email address
type userValue struct{ email string }

func (user userValue) LogValue() slog.Value {
	return slog.GroupValue(slog.String("email", user.email))
}

// stringer prints as its text
type stringer struct{ text string }

func (value stringer) String() string { return value.text }
