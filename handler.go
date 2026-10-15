package telemetry

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/trace"
)

// TextHandler writes the records at minimum and above to w as text, with the
// trace and span ids, as Setup does without an OTLP endpoint. Tests can use it to
// read what a service logs.
func TextHandler(w io.Writer, minimum slog.Level) slog.Handler {
	return &contextHandler{
		next:     slog.NewTextHandler(w, &slog.HandlerOptions{Level: minimum}),
		minimum:  minimum,
		traceIDs: true,
	}
}

// logAttrsKey is the context key of the attributes WithLogAttrs adds
type logAttrsKey struct{}

// WithLogAttrs returns ctx with attrs added to each record written with it, such
// as a job's id
func WithLogAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	previous, _ := ctx.Value(logAttrsKey{}).([]slog.Attr)
	return context.WithValue(ctx, logAttrsKey{}, append(slices.Clip(previous), attrs...))
}

// requestAttrsKey is the context key of a request's requestAttrs
type requestAttrsKey struct{}

// requestAttrs are the attributes the handlers add to a request's logs. The
// request's own line, written once they have run, has them too.
type requestAttrs struct {
	mutex sync.Mutex
	attrs []slog.Attr
}

// WithRequest returns ctx with a place for the attributes AddLogAttrs adds. The
// ginlog and httplog middleware call it for each request.
func WithRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestAttrsKey{}, &requestAttrs{})
}

// AddLogAttrs adds attrs, such as the request id or the signed-in user's id, to
// the records written with the request's context from now on, and to the line
// the request is logged with. Outside a request it does nothing.
func AddLogAttrs(ctx context.Context, attrs ...slog.Attr) {
	if holder, ok := ctx.Value(requestAttrsKey{}).(*requestAttrs); ok {
		holder.mutex.Lock()
		holder.attrs = append(holder.attrs, attrs...)
		holder.mutex.Unlock()
	}
}

// contextAttrs returns the attributes WithLogAttrs and AddLogAttrs added to ctx
func contextAttrs(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(logAttrsKey{}).([]slog.Attr)
	if holder, ok := ctx.Value(requestAttrsKey{}).(*requestAttrs); ok {
		holder.mutex.Lock()
		attrs = append(slices.Clip(attrs), holder.attrs...)
		holder.mutex.Unlock()
	}
	return attrs
}

// emailPattern matches an email address
var emailPattern = regexp.MustCompile(`[^\s<>"'(),;:/@]+@[^\s<>"'(),;:/@]+\.[^\s<>"'(),;:/@]+`)

// redactEmails replaces the email addresses in value with <email>
func redactEmails(value string) string {
	if !strings.Contains(value, "@") {
		return value
	}
	return emailPattern.ReplaceAllString(value, "<email>")
}

// redactAttr replaces the email addresses in attr's string values, in groups too
func redactAttr(attr slog.Attr) slog.Attr {
	switch attr.Value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, redactEmails(attr.Value.String()))
	case slog.KindGroup:
		group := attr.Value.Group()
		redacted := make([]any, len(group))
		for i, member := range group {
			redacted[i] = redactAttr(member)
		}
		return slog.Group(attr.Key, redacted...)
	case slog.KindAny:
		if err, ok := attr.Value.Any().(error); ok {
			return slog.String(attr.Key, redactEmails(err.Error()))
		}
	}
	return attr
}

// contextHandler drops the records below the minimum level and adds the
// attributes from WithLogAttrs and AddLogAttrs, and the trace and span ids when
// traceIDs is set, from the record's context. It replaces the email addresses in
// the message and the string values, such as one an SMTP server quotes in an
// error, so logs never hold them.
type contextHandler struct {
	next     slog.Handler
	minimum  slog.Level
	traceIDs bool
}

func (handler *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= handler.minimum && handler.next.Enabled(ctx, level)
}

func (handler *contextHandler) Handle(ctx context.Context, original slog.Record) error {
	record := slog.NewRecord(
		original.Time,
		original.Level,
		redactEmails(original.Message),
		original.PC,
	)
	original.Attrs(func(attr slog.Attr) bool {
		record.AddAttrs(redactAttr(attr))
		return true
	})
	for _, attr := range contextAttrs(ctx) {
		// Some come from callers, such as an X-Request-ID
		record.AddAttrs(redactAttr(attr))
	}
	if handler.traceIDs {
		if span := trace.SpanContextFromContext(ctx); span.IsValid() {
			record.AddAttrs(slog.String("trace_id", span.TraceID().String()),
				slog.String("span_id", span.SpanID().String()))
		}
	}
	return handler.next.Handle(ctx, record)
}

func (handler *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redacted[i] = redactAttr(attr)
	}
	return &contextHandler{
		next:     handler.next.WithAttrs(redacted),
		minimum:  handler.minimum,
		traceIDs: handler.traceIDs,
	}
}

func (handler *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{
		next:     handler.next.WithGroup(name),
		minimum:  handler.minimum,
		traceIDs: handler.traceIDs,
	}
}
