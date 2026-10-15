// Package httplog traces and logs the requests a net/http server answers, with
// the logs set up by the telemetry package
package httplog

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
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
// how it ended. The line has the route that matched (http.route) when handler is
// the http.ServeMux itself, which sets it on the request.
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
		var status int
		// Keeps the writer's other interfaces, such as http.Flusher and http.Hijacker
		recorder := httpsnoop.Wrap(writer, httpsnoop.Hooks{
			WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
				return func(code int) {
					// 1xx answers, such as 103 Early Hints, come before the final one
					if status == 0 && code >= http.StatusOK {
						status = code
					}
					next(code)
				}
			},
			Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
				return func(body []byte) (int, error) {
					if status == 0 {
						status = http.StatusOK
					}
					return next(body)
				}
			},
			ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
				return func(source io.Reader) (int64, error) {
					if status == 0 {
						status = http.StatusOK
					}
					return next(source)
				}
			},
		})
		// Where the handlers add attributes, such as the user id
		request = request.WithContext(telemetry.WithRequest(request.Context()))
		defer func() {
			recovered := recover()
			if recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				telemetry.LogPanic(request.Context(), recovered)
				if status == 0 {
					options.OnPanic(recorder)
				}
			}
			if request.URL.Path == telemetry.HealthPath {
				return
			}
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
