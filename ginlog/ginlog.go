// Package ginlog traces and logs the requests a gin server answers, with the
// logs set up by the telemetry package
package ginlog

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel/propagation"

	"github.com/Update-Prompt/go-telemetry"
)

// Middleware returns the middleware that traces each request and logs it at the
// level for how it ended. When public is set, the trace id a caller sends is
// ignored and each request starts its own trace. onPanic answers a request whose
// handler panicked, after the panic is logged.
func Middleware(
	serviceName string,
	public bool,
	levels telemetry.RequestLevels,
	onPanic func(*gin.Context),
) []gin.HandlerFunc {
	options := []otelgin.Option{otelgin.WithFilter(func(request *http.Request) bool {
		return request.URL.Path != telemetry.HealthPath
	})}
	if public {
		options = append(
			options,
			otelgin.WithPropagators(propagation.NewCompositeTextMapPropagator()),
		)
	}
	return []gin.HandlerFunc{
		otelgin.Middleware(serviceName, options...),
		logRequests(levels, onPanic),
	}
}

// LogAttrs adds attrs, such as the signed-in user's id, to the records written
// with the request's context from now on, and to the line the request is logged with
func LogAttrs(ctx *gin.Context, attrs ...slog.Attr) {
	telemetry.AddLogAttrs(ctx.Request.Context(), attrs...)
}

// RequestID adds the request's id to its logs, as request_id
func RequestID(ctx *gin.Context, requestID string) {
	LogAttrs(ctx, slog.String("request_id", requestID))
}

// logRequests logs each request but the health checks once it has been
// answered, and recovers from a panic in its handler
func logRequests(levels telemetry.RequestLevels, onPanic func(*gin.Context)) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		start := time.Now()
		// Where the handlers add attributes, such as the user id
		ctx.Request = ctx.Request.WithContext(telemetry.WithRequest(ctx.Request.Context()))
		defer func() {
			recovered := recover()
			if recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				telemetry.LogPanic(ctx.Request.Context(), recovered)
				// Unless the handler had started its answer already
				if !ctx.Writer.Written() {
					onPanic(ctx)
				}
			}
			if ctx.Request.URL.Path == telemetry.HealthPath {
				return
			}
			telemetry.LogRequest(ctx.Request.Context(), levels, telemetry.Request{
				Method:   ctx.Request.Method,
				Path:     ctx.Request.URL.Path,
				Route:    ctx.FullPath(),
				Status:   ctx.Writer.Status(),
				Duration: time.Since(start),
				Panicked: recovered != nil,
			})
		}()
		ctx.Next()
	}
}
