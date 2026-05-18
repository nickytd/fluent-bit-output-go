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
| [`feat/bbolt-otlp-queue`](https://github.com/nickytd/fluent-bit-output-go/tree/feat/bbolt-otlp-queue) | bbolt | Single-file B+ tree (etcd's fork of BoltDB), monotonic uint64 keys, `sync.Cond` signaling |

