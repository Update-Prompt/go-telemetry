package httplog_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Update-Prompt/go-telemetry"
	"github.com/Update-Prompt/go-telemetry/httplog"
	"github.com/Update-Prompt/go-telemetry/internal/testlogs"
)

// Given a handler that adds a user id to the logs, logs a line and answers 500
// When it is requested
// Then the request is logged at error with its method, path, status and the user id, in the same trace as the handler's line
func TestHTTPLogsAFailedRequestWithTheHandlersAttributes(t *testing.T) {
	_, logs := serve(t, false, nil, "/v1/failed")

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected the handler's line and the request's, got %q", logs.String())
	}
	request := lines[1]
	for _, want := range []string{"level=ERROR", "msg=\"Answered a request\"", "http.request.method=GET",
		"url.path=/v1/failed", "http.response.status_code=500", "user_id=user_1"} {
		if !strings.Contains(request, want) {
			t.Errorf("Expected %s in %q", want, request)
		}
	}
	// Both lines are of the same trace, and carry the user id
	traceID := request[strings.Index(request, "trace_id="):][:len("trace_id=")+32]
	if !strings.Contains(lines[0], traceID) || !strings.Contains(lines[0], "user_id=user_1") {
		t.Errorf(
			"Expected the handler's line to carry %s and the user id, got %q",
			traceID,
			lines[0],
		)
	}
}

// Given a handler that answers 4xx and a path function that hides the path's id
// When it is requested
// Then the request is logged at info with the hidden path and the matched route
func TestHTTPLogsTheRequestsPathAsGiven(t *testing.T) {
	_, logs := serve(t, false, nil, "/v1/refused/secret")

	line := logs.String()
	if !strings.Contains(line, "level=INFO") ||
		!strings.Contains(line, "url.path=/v1/refused/{id}") ||
		!strings.Contains(line, "http.route=/v1/refused/{id}") ||
		strings.Contains(line, "secret") {
		t.Errorf("Expected the refused request at info, with its path replaced, got %q", line)
	}
}

// Given a handler that answers 200, succeeded requests logged at debug and logs at info, and the health check
// When each is requested
// Then nothing is logged
func TestHTTPSkipsBelowTheLevelAndTheHealthCheck(t *testing.T) {
	for _, path := range []string{"/v1/ok", "/health"} {
		if _, logs := serve(t, false, nil, path); logs.Len() != 0 {
			t.Errorf("Expected %s not to be logged, got %q", path, logs.String())
		}
	}
}

// Given a handler that panics before answering
// When it is requested
// Then onPanic answers 500, and the panic with its stack and the failed request are logged
func TestHTTPRecoversFromAPanic(t *testing.T) {
	recorder, logs := serve(t, false, nil, "/v1/panics")

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("Expected onPanic's 500, got %d", recorder.Code)
	}
	line := logs.String()
	if !strings.Contains(line, "A request's handler panicked") ||
		!strings.Contains(line, "panic=\"nil map\"") ||
		!strings.Contains(line, "stack=") ||
		!strings.Contains(line, "http.response.status_code=500") {
		t.Errorf("Expected the panic and the failed request in the logs, got %q", line)
	}
}

// Given a handler that panics after it started answering 200
// When it is requested
// Then its answer is left as it was, and the panic and the request are logged at error
func TestHTTPLogsAPanicAfterTheAnswerAsAnError(t *testing.T) {
	recorder, logs := serve(t, false, nil, "/v1/panics-after-answering")

	if recorder.Code != http.StatusOK || recorder.Body.String() != "partial" {
		t.Errorf(
			"Expected the handler's answer to be left as it was, got %d %q",
			recorder.Code,
			recorder.Body.String(),
		)
	}
	line := logs.String()
	if !strings.Contains(line, "A request's handler panicked") ||
		!strings.Contains(line, "level=ERROR msg=\"Answered a request\"") {
		t.Errorf("Expected the panic and the request at error, got %q", line)
	}
}

// Given a request with a caller's traceparent header
// When an internal and a public service answer it
// Then the internal one keeps the caller's trace id and the public one starts its own
func TestHTTPContinuesTheCallersTrace(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	header := http.Header{"Traceparent": {"00-" + traceID + "-00f067aa0ba902b7-01"}}

	_, internal := serve(t, false, header, "/v1/failed")
	_, public := serve(t, true, header, "/v1/failed")

	if !strings.Contains(internal.String(), "trace_id="+traceID) {
		t.Errorf(
			"Expected an internal service to continue the caller's trace, got %q",
			internal.String(),
		)
	}
	if strings.Contains(public.String(), traceID) {
		t.Errorf("Expected a public service to start its own trace, got %q", public.String())
	}
}

// Helpers

// serve answers a request to path with a handler using the middleware, and returns the logs
func serve(
	t *testing.T,
	public bool,
	header http.Header,
	path string,
) (*httptest.ResponseRecorder, *bytes.Buffer) {
	t.Helper()
	logs := testlogs.Use(t, slog.LevelInfo)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/v1/ok", func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("{}"))
	})
	mux.HandleFunc("GET /v1/refused/{id}", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/v1/failed", func(writer http.ResponseWriter, request *http.Request) {
		httplog.LogAttrs(request, slog.String("user_id", "user_1"))
		slog.ErrorContext(request.Context(), "Failed to read a user")
		writer.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/v1/panics", func(writer http.ResponseWriter, request *http.Request) {
		panic("nil map")
	})
	mux.HandleFunc(
		"/v1/panics-after-answering",
		func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte("partial"))
			panic("nil map")
		},
	)
	handler := httplog.Handler(mux, "test-service", httplog.Options{
		Levels: telemetry.RequestLevels{Succeeded: slog.LevelDebug, Refused: slog.LevelInfo},
		Public: public,
		Path: func(request *http.Request) string {
			return strings.Replace(request.URL.Path, "secret", "{id}", 1)
		},
	})

	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, values := range header {
		request.Header[name] = values
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder, logs
}
