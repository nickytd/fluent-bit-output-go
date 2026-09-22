// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

// Package main is the cgo entry point loaded by Fluent Bit via `-e`. The
// //export directives below produce the C-visible symbols Fluent Bit looks
// for in the compiled shared library. All implementation lives under
// internal/ — this file only decodes the incoming msgpack, hands off to the
// convert package, and pushes the result onto the queue.
package main

import (
	"C"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unsafe"

	"github.com/fluent/fluent-bit-go/output"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/nickytd/fluent-bit-output-go/internal/convert"
	"github.com/nickytd/fluent-bit-output-go/internal/exporter"
	"github.com/nickytd/fluent-bit-output-go/internal/flblog"
	"github.com/nickytd/fluent-bit-output-go/internal/queue"
	"github.com/nickytd/fluent-bit-output-go/internal/telemetry"
)

const pluginName = "go-out"

// baseHandler is the slog.Handler used for both instance-scoped and
// plugin-scoped log lines. Built once at load time.
var baseHandler = flblog.NewStderrHandler(pluginName)

var instanceCount int

// telemetrySrv is the shared observability HTTP server. It is created and
// started once on first load (FLBPluginRegister) and then reused across Fluent
// Bit hot-reloads — it is deliberately NOT stopped or rebound on reload, so the
// :2021 endpoint never goes down and the OS port is never re-acquired (which
// used to race the old socket's release and silently disable observability).
// It lives until process exit.
var telemetrySrv *telemetry.Server

// reloadCounter counts observed hot-reloads. It is created once against the
// persistent MeterProvider so its value accumulates across reloads instead of
// resetting. Guarded nil in case the provider is disabled (bind failure).
var reloadCounter metric.Int64Counter

type pluginInstance struct {
	id                 string
	logger             *slog.Logger
	resourceAttributes map[string]struct{}
	queue              *queue.Queue
}

var pluginConfigMap = []output.ConfigMap{
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "id",
		DefValue: "",
		Flags:    0,
		Desc:     "Instance identifier used as a prefix in log lines. Defaults to an auto-incremented integer.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "queue_dir",
		DefValue: "/tmp/fluent-bit-bbolt",
		Flags:    0,
		Desc:     "Absolute path to the directory holding the bbolt queue.db file for persistent buffering.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "otlp_grpc",
		DefValue: "",
		Flags:    0,
		Desc:     "OTLP gRPC endpoint (e.g. localhost:4317). Mutually exclusive with otlp_http.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "otlp_http",
		DefValue: "",
		Flags:    0,
		Desc:     "OTLP HTTP base URL (e.g. http://localhost:4318). /v1/logs is appended automatically. Mutually exclusive with otlp_grpc.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "otlp_http_headers",
		DefValue: "",
		Flags:    0,
		Desc:     "Semicolon-separated extra HTTP headers attached to every OTLP/HTTP request, e.g. \"Authorization=Bearer token;X-Tenant=acme\". Semicolons are used as delimiter so header values may contain commas (e.g. \"VL-Stream-Fields=host.name,severity\").",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "timeout",
		DefValue: "10s",
		Flags:    0,
		Desc:     "Per-request export timeout (e.g. 10s, 1m). Defaults to 10s.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "tls_ca_file",
		DefValue: "",
		Flags:    0,
		Desc:     "Path to a PEM-encoded CA certificate file for verifying the remote endpoint.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "tls_cert_file",
		DefValue: "",
		Flags:    0,
		Desc:     "Path to a PEM-encoded client certificate file for mTLS. Requires tls_key_file.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "tls_key_file",
		DefValue: "",
		Flags:    0,
		Desc:     "Path to a PEM-encoded client key file for mTLS. Requires tls_cert_file.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "tls_insecure_skip_verify",
		DefValue: "",
		Flags:    0,
		Desc:     "Skip TLS certificate verification (true/false). For testing only.",
	},
	{
		Type:     output.FLB_CONFIG_MAP_STR,
		Name:     "resource_attributes",
		DefValue: "",
		Flags:    0,
		Desc:     "Comma-separated record field names to promote to OTLP resource attributes instead of log record attributes (e.g. \"host.name,k8s.namespace.name\"). Resource attributes become stream fields in VictoriaLogs.",
	},
}

//export FLBPluginRegister
func FLBPluginRegister(def unsafe.Pointer) int {
	v, c := buildInfo()
	logger := slog.New(baseHandler)
	logger.Info("loading plugin", "version", v, "commit", c)

	addr := os.Getenv("FLB_GO_OUT_DEBUG_ADDR")
	if addr == "" {
		addr = ":2021"
	}
	// The telemetry server is a process-wide singleton. Create and start it only
	// on first load; on a hot-reload reuse the running server rather than
	// stopping and rebinding it (that stop+rebind used to race the OS releasing
	// the :2021 socket and permanently disable observability — see issue #39).
	if telemetrySrv == nil {
		telemetrySrv = telemetry.New(addr, logger)
		if err := telemetrySrv.Start(); err != nil {
			// Start only returns non-nil for unexpected errors; bind failures are
			// swallowed and logged inside Start itself. Continue without telemetry.
			logger.Warn("telemetry server start error", "err", err)
		}
		// Create the reload counter against the now-persistent provider. This is
		// the first load, so the counter starts at 0 and is not incremented here.
		reloadCounter = newReloadCounter(telemetrySrv.MeterProvider())
	} else {
		// Hot-reload: keep the existing server. The listener is never rebound,
		// so a changed FLB_GO_OUT_DEBUG_ADDR has no effect until process restart.
		if reloadCounter != nil {
			reloadCounter.Add(context.Background(), 1)
		}
	}

	return output.FLBPluginRegisterWithEventTypeAndConfigMap(def, output.FLB_OUTPUT_LOGS, pluginName, "Go OTLP output plugin", pluginConfigMap)
}

//export FLBPluginInit
func FLBPluginInit(plugin unsafe.Pointer) int {
	id := output.FLBPluginConfigKey(plugin, "id")
	if id == "" {
		id = fmt.Sprintf("%d", instanceCount)
		instanceCount++
	} else if err := validateInstanceID(id); err != nil {
		// id is turned into the bbolt filename (<queue_dir>/<id>.db) and reused
		// as a metric label and log prefix, so reject unsafe values outright
		// rather than sanitising them (which would silently change the filename
		// and orphan any existing on-disk queue).
		slog.New(baseHandler).Error("invalid id", "id", id, "err", err)
		return output.FLB_ERROR
	}

	inst := &pluginInstance{
		id:                 id,
		logger:             slog.New(baseHandler.WithGroup(id)),
		resourceAttributes: parseCSVSet(output.FLBPluginConfigKey(plugin, "resource_attributes")),
	}
	queueDir := output.FLBPluginConfigKey(plugin, "queue_dir")
	if queueDir == "" {
		queueDir = "/tmp/fluent-bit-bbolt"
	}
	// Require an absolute queue_dir: a relative path is resolved against
	// fluent-bit's working directory, which can differ across restarts and
	// silently break the persistence guarantee (records must land in the same
	// place every start).
	if !filepath.IsAbs(queueDir) {
		inst.logger.Error("queue_dir must be an absolute path", "queue_dir", queueDir)
		return output.FLB_ERROR
	}

	otlpHTTP := output.FLBPluginConfigKey(plugin, "otlp_http")
	otlpGRPC := output.FLBPluginConfigKey(plugin, "otlp_grpc")

	if otlpHTTP != "" && otlpGRPC != "" {
		inst.logger.Error("only one of otlp_http or otlp_grpc can be set")
		return output.FLB_ERROR
	}

	timeoutRaw := output.FLBPluginConfigKey(plugin, "timeout")
	if timeoutRaw == "" {
		timeoutRaw = "10s"
	}
	timeout, err := exporter.ParseTimeout(timeoutRaw)
	if err != nil {
		inst.logger.Error("invalid timeout", "err", err)
		return output.FLB_ERROR
	}

	tlsCfg, err := exporter.NewDynamicTLSConfig(exporter.TLSSettings{
		CAFile:             output.FLBPluginConfigKey(plugin, "tls_ca_file"),
		CertFile:           output.FLBPluginConfigKey(plugin, "tls_cert_file"),
		KeyFile:            output.FLBPluginConfigKey(plugin, "tls_key_file"),
		InsecureSkipVerify: output.FLBPluginConfigKey(plugin, "tls_insecure_skip_verify") == "true",
	})
	if err != nil {
		inst.logger.Error("invalid TLS config", "err", err)
		return output.FLB_ERROR
	}

	// telemetrySrv is always non-nil here: FLBPluginRegister initialises it
	// before Fluent Bit calls FLBPluginInit. The nil guard is defensive.
	var mp metric.MeterProvider = noop.NewMeterProvider()
	if telemetrySrv != nil {
		mp = telemetrySrv.MeterProvider()
	}

	var exp exporter.Exporter
	switch {
	case otlpGRPC != "":
		exp, err = exporter.NewGRPC(otlpGRPC, timeout, tlsCfg, mp)
		if err != nil {
			inst.logger.Error("failed to create grpc exporter", "err", err)
			return output.FLB_ERROR
		}
	case otlpHTTP != "":
		headers, err := exporter.ParseHeaders(output.FLBPluginConfigKey(plugin, "otlp_http_headers"))
		if err != nil {
			inst.logger.Error("invalid otlp_http_headers", "err", err)
			return output.FLB_ERROR
		}
		exp = exporter.NewHTTP(otlpHTTP, timeout, tlsCfg, mp, headers)
	default:
		exp = exporter.NewStdout()
	}

	q, err := queue.NewWithID(inst.logger, queueDir, id, exp, mp)
	if err != nil {
		inst.logger.Error("failed to init queue", "err", err)
		// Shut down the exporter so its connections and goroutines are not leaked.
		_ = exp.Shutdown(context.Background())
		return output.FLB_ERROR
	}
	inst.queue = q

	inst.logger.Info("initialized instance")
	output.FLBPluginSetContext(plugin, inst)
	return output.FLB_OK
}

//export FLBPluginFlushCtx
func FLBPluginFlushCtx(ctx, data unsafe.Pointer, length C.int, tag *C.char) int {
	inst := output.FLBPluginGetContext(ctx).(*pluginInstance)
	dec := output.NewDecoder(data, int(length))

	var records []convert.DecodedRecord
	for {
		ret, ts, record := output.GetRecord(dec)
		if ret != 0 {
			break
		}
		records = append(records, convert.DecodedRecord{Timestamp: ts, Record: record})
	}

	logs := convert.ProcessRecords(records, inst.resourceAttributes)

	// Possible when a flush contains only envelope markers with no log records.
	if logs.ResourceLogs().Len() == 0 {
		return output.FLB_OK
	}

	marshaler := new(plog.ProtoMarshaler)
	b, err := marshaler.MarshalLogs(logs)
	if err != nil {
		inst.logger.Error("marshal error", "err", err)
		return output.FLB_ERROR
	}

	if err := inst.queue.Enqueue(b); err != nil {
		inst.logger.Error("enqueue error", "err", err)
		return output.FLB_RETRY
	}
	return output.FLB_OK
}

//export FLBPluginExitCtx
func FLBPluginExitCtx(ctx unsafe.Pointer) int {
	inst := output.FLBPluginGetContext(ctx).(*pluginInstance)
	inst.logger.Info("exiting instance")
	inst.queue.Shutdown()
	return output.FLB_OK
}

//export FLBPluginUnregister
func FLBPluginUnregister(def unsafe.Pointer) {
	slog.New(baseHandler).Info("unregistering plugin")
	// Reset the auto-id counter so a hot-reload re-issues the same default ids
	// (0, 1, ...) to instances without an explicit id. Without this the counter
	// keeps climbing across reloads, changing each instance's id — and thus its
	// bbolt filename — which would orphan its un-drained on-disk queue.
	instanceCount = 0
	// Deliberately do NOT stop telemetrySrv here. Fluent Bit calls Unregister on
	// both hot-reload and real shutdown and does not distinguish them; stopping
	// the server on reload is what caused the :2021 rebind race (issue #39). The
	// server is a process-wide singleton left running until the process exits —
	// the OS reclaims the port at exit. The pull-based Prometheus reader has no
	// buffered data to flush, so skipping MeterProvider.Shutdown is harmless.
	output.FLBPluginUnregister(def)
}

func main() {}

// newReloadCounter builds the hot-reload counter against the shared, persistent
// MeterProvider. Returns nil (and the caller nil-guards Add) if instrument
// creation fails; a noop provider yields a working no-op counter.
func newReloadCounter(mp metric.MeterProvider) metric.Int64Counter {
	c, err := mp.Meter("flbgoout/plugin").Int64Counter(
		"flbgoout.plugin.reloads",
		metric.WithDescription("Total number of Fluent Bit plugin hot-reloads observed since process start."),
		metric.WithUnit("{reload}"),
	)
	if err != nil {
		slog.New(baseHandler).Warn("failed to create reload counter", "err", err)
		return nil
	}
	return c
}

// validInstanceID matches ids that are safe to embed in a filename, a metric
// label, and a log prefix: ASCII letters/digits and a small set of separators,
// no path separators. Length is capped separately.
var validInstanceID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateInstanceID rejects ids that could escape queue_dir or produce an
// unusable bbolt filename. It disallows path separators (so "../" traversal is
// impossible), limits the character set, and caps the length.
func validateInstanceID(id string) error {
	if len(id) > 64 {
		return fmt.Errorf("too long (%d chars, max 64)", len(id))
	}
	if !validInstanceID.MatchString(id) {
		return fmt.Errorf("must match [A-Za-z0-9._-]+")
	}
	return nil
}

func parseCSVSet(s string) map[string]struct{} {
	m := make(map[string]struct{})
	for field := range strings.SplitSeq(s, ",") {
		if f := strings.TrimSpace(field); f != "" {
			m[f] = struct{}{}
		}
	}
	return m
}
