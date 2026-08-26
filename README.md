# fluent-bit-output-go

[![CI](https://github.com/nickytd/fluent-bit-output-go/actions/workflows/ci.yml/badge.svg)](https://github.com/nickytd/fluent-bit-output-go/actions/workflows/ci.yml)
[![Release](https://github.com/nickytd/fluent-bit-output-go/actions/workflows/release.yml/badge.svg)](https://github.com/nickytd/fluent-bit-output-go/actions/workflows/release.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/nickytd/fluent-bit-output-go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/nickytd/fluent-bit-output-go?style=flat)](https://github.com/nickytd/fluent-bit-output-go/releases)

A [Fluent Bit](https://fluentbit.io/) output plugin written in Go, compiled as
a C shared library. It converts log records from Fluent Bit's pipeline into
[OpenTelemetry](https://opentelemetry.io/) `plog.Logs` structures, buffers them
in a persistent [bbolt](https://github.com/etcd-io/bbolt) queue on the local
disk, and forwards them to a configurable OTLP target (gRPC, HTTP, or stdout
for debugging).

The plugin is distributed as a container image published to GHCR and is
intended to run as a Kubernetes initContainer that copies the compiled `.so`
into a shared volume for a co-located `fluent-bit` container to load with `-e`.

## Features

- Handles **standard flat records** — each record becomes a `LogRecord` inside
  a `ResourceLogs` with fields mapped as attributes.
- Handles **OpenTelemetry envelope log groups** — Fluent Bit's grouping
  mechanism that preserves resource and instrumentation-scope metadata across
  records in a batch.
- Maps well-known fields:
  - `body` / `log` / `message` → `LogRecord.Body`
  - `severity_number` → `LogRecord.SeverityNumber`
  - `severity_text` → `LogRecord.SeverityText`
  - `level` → both severity text and severity number (via a level-to-number
    lookup: `debug`/`info`/`warn`/`error`/…)
  - `trace_id` (hex string) → `LogRecord.TraceID`
  - `span_id` (hex string) → `LogRecord.SpanID`
  - everything else → `LogRecord.Attributes`
- **Persistent queue** via bbolt B+ tree — records survive plugin/process
  restarts and endpoint outages, then drain in FIFO order when the endpoint
  recovers. Delivery semantics are **at-least-once**: a record enqueued to
  bbolt is only removed after `exp.Export` returns nil, and a crash after
  export but before delete replays the record on the next start.
- **Configurable export target**: stdout (OTLP JSON, for debugging), OTLP/HTTP,
  or OTLP/gRPC.

## Requirements

Runtime:

- Fluent Bit (the plugin is loaded via `-e /path/to/go-out.so`)

Build / development:

- Go 1.27+
- gcc (for the cgo build of `-buildmode=c-shared`)
- [OTel Collector](https://opentelemetry.io/docs/collector/) (`otelcol`
  binary, only for the e2e tests)

Dev tooling (linters, test runner, security scanners) is managed via
`tools/go.mod` and invoked through `go tool -modfile=tools/go.mod` — no
separate installs required.

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
      # queue_dir: /tmp/fluent-bit-bbolt
      # otlp_grpc: localhost:4317
      # otlp_http: http://localhost:4318
```

### Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `id` | auto-increment | Instance identifier used as a prefix in log lines |
| `queue_dir` | `/tmp/fluent-bit-bbolt` | Directory holding the bbolt `queue.db` file |
| `otlp_grpc` | *(none)* | OTLP gRPC endpoint (e.g. `localhost:4317`) |
| `otlp_http` | *(none)* | OTLP HTTP base URL (e.g. `http://localhost:4318`; `/v1/logs` is appended automatically) |

If neither `otlp_grpc` nor `otlp_http` is set, records are emitted as OTLP
JSON on stdout — useful for local debugging. Setting both is rejected at
init time.

## Testing with an OTel Collector

An example collector config is provided in `otel-collector.yaml`:

```bash
# Start the collector: receives on gRPC :4317 and HTTP :4318, prints via
# the debug exporter.
otelcol --config otel-collector.yaml

# In another terminal, start fluent-bit with the plugin. The default
# fluent-bit.yaml uses otlp_http; to test gRPC instead, edit fluent-bit.yaml
# so `otlp_grpc: localhost:4317` is set and `otlp_http` is commented out.
make run
```

## Testing

```bash
make unit-test    # unit tests
make e2e-test     # builds .so, starts otelcol, runs fluent-bit, asserts output
make test         # both
```

CI runs unit tests with the race detector via `make unit-test TEST_ARGS="-race -count=1"`.
To match CI locally, run the same command. Bare `go test -race ./...` also works but
bypasses `gotestsum` and the tools module.

E2E tests require `fluent-bit` and `otelcol` on PATH. They start an OTel
Collector with the debug exporter, run Fluent Bit with the plugin exporting
via OTLP/HTTP, and assert that log records (resource attributes, severity,
body) appear in the collector's output.

## Architecture

```
Fluent Bit ─▶ FLBPluginFlushCtx ─▶ processRecords() ─▶ plog.Logs
                                                          │
                    ProtoMarshaler ◀─────────────────────┘
                          │
                          ▼
          bbolt DB (single "logs" bucket, uint64 keys)
                          │
                          ▼
      consumer goroutine (sync.Cond wake) ─▶ ProtoUnmarshaler
                          │
                          ▼
              exporter.Export(ctx, plog.Logs)
                          │
        ┌─────────────────┼─────────────────┐
        ▼                 ▼                 ▼
   stdout (JSON)     OTLP/HTTP          OTLP/gRPC
                    (plogotlp)          (plogotlp)
```

- **Queue.** bbolt single-file B+ tree (`queue.db`) with one `logs` bucket.
  Big-endian uint64 keys give lexicographic FIFO ordering; each enqueue is a
  single ACID `Update` transaction. On startup the plugin reseeds its
  in-memory `writeSeq` from the bucket's last key so a restart with
  un-drained records never overwrites them.
- **Consumer.** A single background goroutine woken by a `sync.Cond` signal
  after every enqueue. On wake it iterates the bucket and calls
  `exp.Export(ctx, logs)` for each record. On export success the key is
  queued for a batch `Delete` in a separate `Update` transaction. **On
  export failure the key is preserved** and the goroutine enters a capped
  exponential backoff (500 ms → 30 s, cancellable on shutdown) so a
  down endpoint never spins the consumer. Unmarshalable payloads are
  logged and dropped — they will never succeed on retry.
- **Serialization.** `plog.ProtoMarshaler` / `ProtoUnmarshaler` for compact
  binary storage in the queue.

## How It Works

Plugin operational logs go to **stderr** using a custom `slog.Handler` that
matches Fluent Bit's `[ts] [level] [tag] message` format so the plugin's own
logs blend into fluent-bit's console output.

Fluent Bit's `opentelemetry_envelope` processor injects two synthetic records
around each envelope group, distinguished by sentinel Fluent Bit timestamps:

- `0xFFFFFFFF` → group start (carries resource + instrumentation-scope metadata)
- `0xFFFFFFFE` → group end

Records between the markers inherit that group's resource and scope. Records
outside any group get their own empty `ResourceLogs`.

## Container image and Kubernetes deployment

The plugin is published as a multi-arch (`linux/amd64` + `linux/arm64`)
container image on GHCR at `ghcr.io/nickytd/fluent-bit-output-go`.

The image is intentionally minimal — it carries `/plugin/go-out.so` (the
compiled shared library) and a small static `copy-plugin` entrypoint. It is
designed to run as a Kubernetes **initContainer** that copies `go-out.so`
onto a shared `emptyDir`, from which the main `fluent-bit` container then
loads it via `-e`.

### Minimal Pod example

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: fluent-bit
spec:
  initContainers:
    - name: install-plugin
      # Pin to a released tag for reproducible deployments. Also available
      # as :latest (tracks the newest tag), :v0 (major), and :v0.2 (minor).
      image: ghcr.io/nickytd/fluent-bit-output-go:v0.2.0
      volumeMounts:
        - name: plugin
          mountPath: /output
  containers:
    - name: fluent-bit
      image: fluent/fluent-bit:latest
      args:
        - -c
        - /fluent-bit/etc/fluent-bit.yaml
        - -e
        - /fluent-bit/plugins/go-out.so
      volumeMounts:
        - name: plugin
          mountPath: /fluent-bit/plugins
          readOnly: true
        # …plus your fluent-bit config and log volumes.
  volumes:
    - name: plugin
      emptyDir: {}
```

The initContainer's default command is:

```
/copy-plugin -src=/plugin/go-out.so -dst=/output
```

Override `-dst` if your shared volume is mounted at a different path.

A full `DaemonSet` example — including a `ConfigMap` with a fluent-bit YAML
that wires up `go-out` with an OTLP/gRPC exporter and a `hostPath` for the
persistent bbolt queue — lives at [`deploy/kubernetes-daemonset.yaml`](deploy/kubernetes-daemonset.yaml).

### Building the image locally

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t fluent-bit-output-go:local .
```

The `Dockerfile` is a three-stage multi-arch build: one stage cross-compiles
the cgo `.so` against glibc (matching Fluent Bit's official Debian-based
runtime image), one stage builds the static `copy-plugin` binary, and the
final stage assembles both into a `scratch` image.

## Releases

Pushing a semver tag (`v*`) to `main` triggers the release workflow, which
builds the multi-arch image, pushes it to GHCR with three tag aliases
(`vX.Y.Z`, `vX.Y`, `vX`) plus attaches an SBOM and provenance attestation,
and creates a GitHub Release entry with auto-generated notes from the
Conventional Commit history since the previous tag.

```bash
git tag v0.1.0
git push origin v0.1.0
```

Published releases and their generated notes live on the
[Releases page](https://github.com/nickytd/fluent-bit-output-go/releases).

## Persistent Queue Experiments (design history)

The current bbolt design landed after two earlier prototypes on
[dque](https://github.com/joncrlsn/dque) and
[Pebble](https://github.com/cockroachdb/pebble). Both prototype branches
have been retired; the table below is kept as design-decision context for
readers evaluating alternative KV backends.

| Aspect | [dque](https://github.com/joncrlsn/dque) | [Pebble](https://github.com/cockroachdb/pebble) | bbolt (current) |
|--------|------|--------|------------------------|
| **Backend** | Segmented flat files + gob | LSM-tree (SSTables + WAL) | B+ tree (single file) |
| **Serialization** | `encoding/gob` (public fields only) | Raw `[]byte` (no opinion) | Protobuf |
| **Queue semantics** | Built-in FIFO (`Enqueue`/`DequeueBlock`) | Manual (sequence keys + iterator) | Manual (uint64 keys + cursor) |
| **Blocking dequeue** | Native (`DequeueBlock`) | Must implement (`sync.Cond`) | Must implement (`sync.Cond`) |
| **Space reclamation** | Automatic (segment file deletion) | Automatic (leveled compaction) | Manual (bbolt compaction) |
| **Write throughput** | Moderate (file-per-segment) | High (WAL + memtable batching) | Moderate (single-writer B+ tree) |
| **Crash safety** | WAL per segment | WAL (configurable sync) | ACID transactions |
| **Maintenance** | Abandoned (last commit 2024) | Active (CockroachDB production) | Active (etcd project) |
| **Dependency weight** | Light (~3 deps) | Moderate (~15 deps) | Light (~5 deps) |

## License

Apache-2.0 — see [LICENSE](LICENSE) and [LICENSES/](LICENSES/).
