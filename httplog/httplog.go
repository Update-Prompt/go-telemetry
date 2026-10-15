// Package httplog traces and logs the requests a net/http server answers, with
// the logs set up by the telemetry package
package httplog

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"

	"github.com/Update-Prompt/go-telemetry"
)

// Options are how Handler traces and logs requests
type Options struct {
	// Levels are the levels requests are logged at, by how they ended
	Levels telemetry.RequestLevels
	// Public ignores the trace id a caller sends, so each request starts its own trace
	Public bool
	// Path is what a request's path is logged as, such as with an email address
	// in it replaced. The path itself when nil.
	Path func(*http.Request) string
	// OnPanic answers a request whose handler panicked, after the panic is
	// logged, unless the handler had started its answer. A plain 500 when nil.
	OnPanic func(http.ResponseWriter)
}

// Handler returns handler, tracing each request and logging it at the level for
// how it ended
func Handler(handler http.Handler, serviceName string, options Options) http.Handler {
	if options.Path == nil {
		options.Path = func(request *http.Request) string { return request.URL.Path }
	}
	if options.OnPanic == nil {
		options.OnPanic = func(writer http.ResponseWriter) {
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}
	otelOptions := []otelhttp.Option{otelhttp.WithFilter(func(request *http.Request) bool {
		return request.URL.Path != telemetry.HealthPath
	})}
	if options.Public {
		otelOptions = append(
			otelOptions,
			otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator()),
		)
	}
	return otelhttp.NewHandler(logRequests(handler, options), serviceName, otelOptions...)
}

// LogAttrs adds attrs, such as the signed-in user's id, to the records written
// with the request's context from now on, and to the line the request is logged with
func LogAttrs(request *http.Request, attrs ...slog.Attr) {
	telemetry.AddLogAttrs(request.Context(), attrs...)
}

// logRequests logs each request but the health checks once it has been
// answered, and recovers from a panic in its handler
func logRequests(handler http.Handler, options Options) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer}
		// Where the handlers add attributes, such as the user id
		request = request.WithContext(telemetry.WithRequest(request.Context()))
		defer func() {
			recovered := recover()
			if recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				telemetry.LogPanic(request.Context(), recovered)
				if recorder.status == 0 {
					options.OnPanic(recorder)
				}
			}
			if request.URL.Path == telemetry.HealthPath {
				return
			}
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			telemetry.LogRequest(request.Context(), options.Levels, telemetry.Request{
				Method:   request.Method,
				Path:     options.Path(request),
				Route:    route(request.Pattern),
				Status:   status,
				Duration: time.Since(start),
				Panicked: recovered != nil,
			})
		}()
		handler.ServeHTTP(recorder, request)
	})
}

// route returns the path part of a ServeMux pattern, such as /v1/users/{id}
// from "GET /v1/users/{id}"
func route(pattern string) string {
	if start := strings.Index(pattern, "/"); start >= 0 {
		return pattern[start:]
	}
	return ""
}

// statusRecorder remembers the status a handler answered with
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.status == 0 {
		recorder.status = status
	}
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach the writer's other methods, such as Flush
func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}
