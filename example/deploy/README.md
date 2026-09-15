# Kubernetes deployment example

This directory contains a minimal Kubernetes example for running the
`go-out` Fluent Bit output plugin as a DaemonSet and exporting logs to an
OpenTelemetry Collector.

## Files

| File | Description |
|------|-------------|
| `kubernetes-daemonset.yaml` | Creates the `observability` namespace, RBAC, Fluent Bit configuration, and a DaemonSet that loads `go-out.so` from a plugin initContainer. |
| `otelcol.yaml` | Creates an `OpenTelemetryCollector` custom resource that receives OTLP/gRPC logs from Fluent Bit on port `4317` and writes them with the debug exporter. |

## Prerequisites

- A Kubernetes cluster with access to node container logs under `/var/log`.
- The [OpenTelemetry Operator](https://opentelemetry.io/docs/platforms/kubernetes/operator/) installed, because `otelcol.yaml` uses the `OpenTelemetryCollector` custom resource.
- Nodes that can pull `ghcr.io/nickytd/fluent-bit-output-go:v0.10.1` and `fluent/fluent-bit:5.1.2`.

## Deploy

Apply the collector first, then the Fluent Bit DaemonSet:

```bash
kubectl apply -f otelcol.yaml
kubectl apply -f kubernetes-daemonset.yaml
```

Check that the collector and Fluent Bit pods are running:

```bash
kubectl -n observability get pods
```

Inspect collector output:

```bash
kubectl -n observability logs deploy/fluent-bit-collector
```

## How it works

The Fluent Bit DaemonSet uses an initContainer from the plugin image to copy
`/plugin/go-out.so` into a shared `emptyDir` volume. The main `fluent-bit`
container mounts that volume at `/fluent-bit/plugins` and loads the plugin
with:

```text
-e /fluent-bit/plugins/go-out.so
```

The example Fluent Bit configuration tails `/var/log/containers/*.log` and
exports records through the plugin to:

```text
fluent-bit-collector.observability.svc.cluster.local:4317
```

Queued log batches are persisted under `/var/lib/fluent-bit/queue` on each
node through a `hostPath` volume, so they can survive Fluent Bit pod restarts
on the same node.

## Cleanup

```bash
kubectl delete -f kubernetes-daemonset.yaml
kubectl delete -f otelcol.yaml
```
