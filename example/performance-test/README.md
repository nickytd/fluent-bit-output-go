# Performance Test

End-to-end performance test harness for the `go-out` Fluent Bit OTLP output plugin.

## Stack

```
Logger Jobs (nickytd/log-generator) → stdout → /var/log/containers
  → Fluent Bit DaemonSet (go-out plugin)
    → OTel Collector (OTLP gRPC :4317)
      → VictoriaLogs (:9428)

Prometheus → scrapes Fluent Bit :2020, go-out plugin :2021, OTel :8888, VictoriaLogs :9428
Grafana → four dashboards (Fluent Bit, Fluent Bit go-out Plugin, OTel Collector, VictoriaLogs)
```

## Prerequisites

- `kubectl` configured against a target cluster
- `helm` >= 3
- `jq` (used in `check.sh` / `fetch.sh` for JSON parsing)
- `direnv` (optional but recommended — auto-loads `.envrc`)

## Configuration

Default parameters are set in `.envrc`. With `direnv` installed, run `direnv allow` once and all scripts and `make` targets pick them up automatically.

```bash
# .envrc
export NAMESPACE=perf-test
export NAMESPACES=20
export JOBS=20
export LOGS=125000
export LOGS_DELAY=10ms
export VL_ENDPOINT=http://localhost:9428
```

## Quick Start

```bash
# Deploy the stack
make setup

# Run the load test
make run

# Check progress at any time
make check

# Per-namespace breakdown
make fetch

# Teardown
make down    # delete logger jobs/namespaces only
make clean   # full teardown including Helm release and PVCs
```

## Parameters

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `perf-test` | Kubernetes namespace for the stack |
| `HELM_RELEASE` | `fluent-bit-plugin` | Helm release name |
| `NAMESPACES` | `20` | Number of source namespaces with logger Jobs |
| `JOBS` | `20` | Logger Jobs per namespace |
| `LOGS` | `125000` | Log lines per Job |
| `LOGS_DELAY` | `10ms` | Delay between log lines (controls per-pod rate) |
| `LOGGER_IMAGE` | `nickytd/log-generator:v0.1.10` | Logger container image |
| `VALUES_FILE` | *(empty)* | Optional path to a Helm values override file |
| `NS` | *(empty)* | Restrict `make fetch` to a single namespace number |
| `VL_ENDPOINT` | `http://victorialogs-http.${NAMESPACE}.svc.cluster.local:9428` | VictoriaLogs endpoint for check/fetch scripts; override to `http://localhost:9428` when port-forwarding |

**Throughput formula:** `NAMESPACES × JOBS × (1000 / LOGS_DELAY_ms)` r/s total  
With defaults: `20 × 20 × 100 = 40,000 r/s`

**Total log count:** `NAMESPACES × JOBS × LOGS` — with defaults `20 × 20 × 125,000 = 50,000,000`

**Job duration:** `LOGS × LOGS_DELAY` — with defaults each job runs for `125,000 × 10ms = 1,250s (~21 min)`

## Makefile Targets

| Target | Description |
|---|---|
| `make setup` | Deploy the Helm chart |
| `make run` | Create namespaces and start logger Jobs |
| `make check` | Show current ingested count vs expected |
| `make fetch [NS=N]` | Per-namespace progress; `NS=3` checks only `ns-3` |
| `make down` | Delete test namespaces and Jobs |
| `make clean` | Uninstall Helm release, delete PVCs and namespace |
| `make test` | `setup` + `run` + `check` |
| `make port-forward-prometheus` | Forward Prometheus to `localhost:9090` |
| `make port-forward-grafana` | Forward Grafana to `localhost:3000` |
| `make port-forward-victorialogs` | Forward VictoriaLogs to `localhost:9428` |

## Querying VictoriaLogs

When port-forwarded to `localhost:9428`:

```bash
# Total ingested count across all logger jobs
curl 'http://localhost:9428/select/logsql/stats_query?query=name%3A~%22ns-.*%22+%7C+stats+count()+as+total&time=now'

# Count for a specific namespace
curl 'http://localhost:9428/select/logsql/stats_query?query=name%3A~%22ns-1-job-.*%22+%7C+stats+count()+as+total&time=now'
```

Or use the scripts directly:

```bash
VL_ENDPOINT=http://localhost:9428 bash check.sh        # total progress
VL_ENDPOINT=http://localhost:9428 bash fetch.sh        # per-namespace
VL_ENDPOINT=http://localhost:9428 bash fetch.sh 3      # namespace 3 only
```

## Dashboards

Grafana is pre-provisioned with four dashboards:

- **Fluent Bit — go-out plugin**: throughput, queue depth, gRPC latency, Go runtime, CPU and memory usage
- **Fluent Bit**: input/output records and bytes, retry and error rates
- **OTel Collector**: pipeline throughput, queue size, memory, export latency
- **VictoriaLogs**: ingestion rate, storage size, query latency

## Overriding Image Versions

```bash
cat > my-values.yaml <<EOF
plugin:
  tag: v0.9.0
fluentbit:
  tag: 5.2.0
EOF
make setup VALUES_FILE=my-values.yaml
```
