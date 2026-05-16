# fluent-bit-output-go

A [Fluent Bit](https://fluentbit.io/) output plugin written in Go, compiled as a C shared library. It converts log records from Fluent Bit's pipeline into [OpenTelemetry](https://opentelemetry.io/) `plog.Logs` structures and emits OTLP JSON to stdout.

## Features

- Handles **standard flat records** — each becomes its own ResourceLogs with fields mapped as LogRecord attributes.
- Handles **OpenTelemetry envelope log groups** — Fluent Bit's internal grouping mechanism that preserves resource and scope metadata.
- Maps well-known OTLP fields: `body`, `severity_number`, `severity_text`, `trace_id`, `span_id`.

## Requirements

- Go 1.26+
- Fluent Bit (for running the plugin and e2e tests)
- [golangci-lint](https://golangci-lint.run/) (for linting)

## Build

```bash
make build    # produces bin/go-out.so
```

## Usage

```bash
make run
# or manually:
fluent-bit -e bin/go-out.so -c fluent-bit.yaml
```

The plugin registers as `go-out`. Configure it in your Fluent Bit pipeline:

```yaml
pipeline:
  outputs:
    - name: go-out
      match: "*"
```

## Testing

```bash
make unit-test    # unit tests
make e2e-test     # builds .so, runs fluent-bit, validates OTLP output
make test         # both
```

## How It Works

OTLP JSON is written to **stdout**. Plugin operational logs go to **stderr** using a custom `slog.Handler` that matches Fluent Bit's log format.

The plugin detects OTel envelope groups via special marker timestamps:
- `0xFFFFFFFF` → group start (carries resource and scope metadata)
- `0xFFFFFFFE` → group end

Records between markers inherit the group's resource/scope. Records outside any group get their own empty ResourceLogs.

## License

MIT
