package ginlog_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Update-Prompt/go-telemetry"
	"github.com/Update-Prompt/go-telemetry/ginlog"
	"github.com/Update-Prompt/go-telemetry/internal/testlogs"
)

// Given a handler that adds the user's id to the logs, logs a line and answers 500
// When it is requested
// Then the request is logged at error with its method, path, route, status, request id and
// the user id the handler added, in the same trace as the handler's line
func TestGinLogsAFailedRequestAsAnError(t *testing.T) {
	_, logs := serve(t, false, nil, "/v1/failed")

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected the handler's line and the request's, got %q", logs.String())
	}
	request := lines[1]
	for _, want := range []string{"level=ERROR", "msg=\"Answered a request\"", "http.request.method=GET",
		"url.path=/v1/failed", "http.route=/v1/failed", "http.response.status_code=500", "request_id=req_1", "user_id=user_1"} {
		if !strings.Contains(request, want) {
			t.Errorf("Expected %s in %q", want, request)
		}
	}
	// Both lines are of the same trace
	traceID := request[strings.Index(request, "trace_id="):][:len("trace_id=")+32]
	if !strings.Contains(lines[0], traceID) {
		t.Errorf("Expected the handler's line to carry %s, got %q", traceID, lines[0])
	}
}

// Given a handler that answers 4xx and refused requests logged at info
// When it is requested
// Then the request is logged at info with its route and path
func TestGinLogsARefusedRequestAtItsLevel(t *testing.T) {
	_, logs := serve(t, false, nil, "/v1/refused/42")

	line := logs.String()
	if !strings.Contains(line, "level=INFO") ||
		!strings.Contains(line, "http.route=/v1/refused/:id") ||
		!strings.Contains(line, "url.path=/v1/refused/42") {
		t.Errorf("Expected the refused request at info, got %q", line)
	}
}

// Given a handler that answers 200, succeeded requests logged at debug and logs at info, and the health check
// When each is requested
// Then nothing is logged
func TestGinSkipsBelowTheLevelAndTheHealthCheck(t *testing.T) {
	for _, path := range []string{"/v1/ok", "/health"} {
		if _, logs := serve(t, false, nil, path); logs.Len() != 0 {
			t.Errorf("Expected %s not to be logged, got %q", path, logs.String())
		}
	}
}

// Given a handler that panics before answering
// When it is requested
// Then onPanic answers 500, and the panic with its stack and the failed request are logged
func TestGinRecoversFromAPanic(t *testing.T) {
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
func TestGinLogsAPanicAfterTheAnswerAsAnError(t *testing.T) {
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

// Given a middleware that panics before the handler of its route
// When the route is requested, with no onPanic given
// Then the answer is a 500 and the handler doesn't run
func TestGinStopsTheChainAfterAPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := testlogs.Use(t, slog.LevelInfo)
	router := gin.New()
	router.Use(ginlog.Middleware("test-service", false, telemetry.RequestLevels{}, nil)...)
	ran := false
	router.GET("/v1/guarded", func(ctx *gin.Context) { panic("nil map") }, func(ctx *gin.Context) {
		ran = true
		ctx.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/guarded", nil))

	if ran || recorder.Code != http.StatusInternalServerError {
		t.Errorf("Expected a 500 without the handler, got %d and ran=%v", recorder.Code, ran)
	}
	if !strings.Contains(logs.String(), "http.response.status_code=500") {
		t.Errorf("Expected the failed request in the logs, got %q", logs.String())
	}
}

// Given a request with a caller's traceparent header
// When an internal service and a public service answer it
// Then the internal one logs the caller's trace id and the public one starts its own trace
func TestGinContinuesTheCallersTrace(t *testing.T) {
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

// serve answers a request to path with a router using the middleware, and returns the logs
func serve(
	t *testing.T,
	public bool,
	header http.Header,
	path string,
) (*httptest.ResponseRecorder, *bytes.Buffer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logs := testlogs.Use(t, slog.LevelInfo)
	router := gin.New()
	levels := telemetry.RequestLevels{Succeeded: slog.LevelDebug, Refused: slog.LevelInfo}
	router.Use(ginlog.Middleware("test-service", public, levels, func(ctx *gin.Context) {
		ctx.AbortWithStatus(http.StatusInternalServerError)
	})...)
	router.Use(func(ctx *gin.Context) {
		ginlog.RequestID(ctx, "req_1")
	})
	router.GET("/health", func(ctx *gin.Context) { ctx.Status(http.StatusServiceUnavailable) })
	router.GET("/v1/ok", func(ctx *gin.Context) { ctx.Status(http.StatusOK) })
	router.GET("/v1/refused/:id", func(ctx *gin.Context) { ctx.Status(http.StatusBadRequest) })
	router.GET("/v1/failed", func(ctx *gin.Context) {
		ginlog.LogAttrs(ctx, slog.String("user_id", "user_1"))
		slog.ErrorContext(ctx.Request.Context(), "Failed to read usage")
		ctx.Status(http.StatusInternalServerError)
	})
	router.GET("/v1/panics", func(ctx *gin.Context) { panic("nil map") })
	router.GET("/v1/panics-after-answering", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "partial")
		panic("nil map")
	})

	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, values := range header {
		request.Header[name] = values
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder, logs
}
