# fluent-bit-output-go

A [Fluent Bit](https://fluentbit.io/) output plugin written in Go, compiled as a C shared library. It converts log records from Fluent Bit's pipeline into [OpenTelemetry](https://opentelemetry.io/) `plog.Logs` structures, buffers them in a persistent [Pebble](https://github.com/cockroachdb/pebble) queue, and exports via configurable OTLP targets.

## Features

- Handles **standard flat records** — each becomes its own ResourceLogs with fields mapped as LogRecord attributes.
- Handles **OpenTelemetry envelope log groups** — Fluent Bit's internal grouping mechanism that preserves resource and scope metadata.
- Maps well-known fields: `body`/`log`/`message` → LogRecord body, `level` → severity, `severity_number`, `severity_text`, `trace_id`, `span_id`.
- **Persistent queue** via Pebble LSM-tree — monotonic sequence keys provide FIFO ordering with crash recovery.
- **Configurable export**: stdout (OTLP JSON), OTLP/HTTP, or OTLP/gRPC.

## Requirements

- Go 1.26+
- Fluent Bit (for running the plugin and e2e tests)
- [OTel Collector](https://opentelemetry.io/docs/collector/) (`otelcol` binary, for e2e tests)
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
      # queue_dir: /tmp/fluent-bit-pebble
      # otlp_grpc: localhost:4317
      # otlp_http: http://localhost:4318
```

### Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `id` | auto-increment | Instance identifier for logging |
| `queue_dir` | `/tmp/fluent-bit-pebble` | Directory for Pebble database storage |
| `otlp_grpc` | *(none)* | OTLP gRPC endpoint (e.g. `localhost:4317`) |
| `otlp_http` | *(none)* | OTLP HTTP endpoint (e.g. `http://localhost:4318`) |

If neither `otlp_grpc` nor `otlp_http` is set, logs are emitted as OTLP JSON to stdout. Only one of `otlp_grpc`/`otlp_http` can be set.

## Testing with OTel Collector

An example collector config is provided in `otel-collector.yaml`:

```bash
# Start the collector (receives on gRPC:4317 and HTTP:4318, prints via debug exporter)
otelcol --config otel-collector.yaml

# In another terminal, run with gRPC export:
# Uncomment otlp_grpc in fluent-bit.yaml, then:
make run
```

## Testing

```bash
make unit-test    # unit tests
make e2e-test     # builds .so, starts OTel Collector, runs fluent-bit, validates output
make test         # both
```

E2E tests require `fluent-bit` and `otelcol` in PATH. They start an OTel Collector with the debug exporter, run Fluent Bit with the plugin exporting via OTLP/HTTP, and verify that log records (including resource attributes and severity) appear in the collector output.

## Architecture

```
Fluent Bit → FLBPluginFlushCtx → processRecords() → plog.Logs
    → ProtoMarshaler → Pebble DB (uint64 key, FIFO)
    → consumer goroutine (sync.Cond) → ProtoUnmarshaler → exporter
                                                            ├── stdout (OTLP JSON)
                                                            ├── OTLP/HTTP (plogotlp)
                                                            └── OTLP/gRPC (plogotlp)
```

- **Queue**: Pebble LSM-tree with big-endian uint64 keys for lexicographic FIFO ordering. `pebble.NoSync` writes (WAL provides crash recovery).
- **Consumer**: `sync.Cond` wait/signal loop — producer signals after `Set`, consumer iterates and deletes processed entries.
- **Serialization**: `plog.ProtoMarshaler`/`ProtoUnmarshaler` for compact binary queue storage.

## How It Works

Plugin operational logs go to **stderr** using a custom `slog.Handler` that matches Fluent Bit's log format.

The plugin detects OTel envelope groups via special marker timestamps:
- `0xFFFFFFFF` → group start (carries resource and scope metadata)
- `0xFFFFFFFE` → group end

Records between markers inherit the group's resource/scope. Records outside any group get their own empty ResourceLogs.

## License

MIT

## Persistent Queue Experiments

This project explores persistent buffering between Fluent Bit's flush pipeline and the OTel SDK output. Instead of writing OTLP JSON directly to stdout, log batches are serialized into an on-disk queue and consumed asynchronously by a goroutine that pushes records through the OTel SDK LoggerProvider.

### Comparison

| Aspect | [dque](https://github.com/joncrlsn/dque) | [Pebble](https://github.com/cockroachdb/pebble) | OTel Collector (bbolt) |
|--------|------|--------|------------------------|
| **Backend** | Segmented flat files + gob | LSM-tree (SSTables + WAL) | B+ tree (single file) |
| **Serialization** | `encoding/gob` (public fields only) | Raw `[]byte` (no opinion) | Protobuf |
| **Queue semantics** | Built-in FIFO (`Enqueue`/`DequeueBlock`) | Manual (sequence keys + iterator) | Manual (read/write index over KV) |
| **Blocking dequeue** | Native (`DequeueBlock`) | Must implement (`sync.Cond`) | Must implement |
| **Space reclamation** | Automatic (segment file deletion) | Automatic (leveled compaction) | Manual (bbolt compaction) |
| **Write throughput** | Moderate (file-per-segment) | High (WAL + memtable batching) | Moderate (single-writer B+ tree) |
| **Crash safety** | WAL per segment | WAL (configurable sync) | ACID transactions |
| **Maintenance** | Abandoned (last commit 2024) | Active (CockroachDB production) | Active (etcd project) |
| **Dependency weight** | Light (~3 deps) | Moderate (~15 deps) | Light (~5 deps) |
| **Best for** | Simple prototyping | High-throughput log buffering | OTel Collector integration |

### Branch Implementations

| Branch | Queue Backend | Description |
|--------|--------------|-------------|
| [`feat/dque-otlp-queue`](https://github.com/nickytd/fluent-bit-output-go/tree/feat/dque-otlp-queue) | dque | Gob-serialized `QueueItem{Data []byte}` with native blocking dequeue |
| [`feat/pebble-otlp-queue`](https://github.com/nickytd/fluent-bit-output-go/tree/feat/pebble-otlp-queue) | Pebble | Monotonic uint64 keys, raw byte values, `sync.Cond` signaling |

