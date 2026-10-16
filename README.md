# go-telemetry

Logs for Update Prompt's Go services, through OpenTelemetry.

- With an OTLP endpoint set, logs go to it as OpenTelemetry logs (OTLP over HTTP).
- Without one, they go to stdout as text.
- Every record written while handling a request carries the request's `trace_id`, `span_id` and the attributes its handlers added, such as `request_id` and `user_id`.
- Email addresses are replaced with `<email>` in every record.
- No spans are recorded or exported: the tracer only gives each request a trace id and passes it on to the services it calls.

## Packages

| Package | What it does |
|---|---|
| `github.com/Update-Prompt/go-telemetry` (package `telemetry`) | `Setup` makes slog's default logger write the service's logs. `WithLogAttrs` and `AddLogAttrs` add attributes to the records of a context. |
| `.../go-telemetry/ginlog` | Middleware that traces and logs each request a gin server answers. |
| `.../go-telemetry/httplog` | The same for a `net/http` handler. |

## Use

```go
shutdown, err := telemetry.Setup(ctx, "usage-service", os.Getenv("LOG_LEVEL"))
if err != nil {
	return err
}
defer shutdown(context.Background()) // sends the logs still buffered

// gin
router := gin.New()
levels := telemetry.RequestLevels{Succeeded: slog.LevelDebug, Refused: slog.LevelInfo}
router.Use(ginlog.Middleware("usage-service", false, levels, onPanic)...)

// net/http
handler := httplog.Handler(mux, "user-management-service", httplog.Options{Levels: levels})
```

Each request is logged once it is answered, as `Answered a request`, at:

| Answer | Level |
|---|---|
| 5xx, or the handler panicked | error, with the panic and its stack |
| 4xx | `RequestLevels.Refused` |
| 1xx to 3xx | `RequestLevels.Succeeded` |
| `GET /health` | not logged or traced |

Set `public` (gin) or `Options.Public` (net/http) on services callers reach from the internet: a caller's `traceparent` is then ignored and each request starts its own trace.

## Configuration

| Variable | Default | What it does |
|---|---|---|
| `LOG_LEVEL` | `info` | Lowest level logged: `debug`, `info`, `warn` or `error` (the service passes it to `Setup`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP endpoint for the logs, such as `http://localhost:4318`; stdout when unset |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | unset | The same, for logs only |
| `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | unset | Override the service name and add resource attributes |

The other standard `OTEL_EXPORTER_OTLP_*` variables, such as headers and timeouts, work too.

## Develop

```sh
go test ./...
```

Lint with `.golangci.yml`, a copy of the shared config in shared-actions (`go-lint/golangci.yml`); keep the two in step. Releases are tags (`vX.Y.Z`); services pin a version in their `go.mod`.

[Renovate](https://docs.renovatebot.com/) keeps the Go modules and the workflows' actions and tools up to date, with the shared Update Prompt preset (`renovate.json`): weekly pull requests, and security fixes right away.
