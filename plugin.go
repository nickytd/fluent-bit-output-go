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
	"strings"
	"time"
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

// telemetrySrv is the shared observability HTTP server started once in
// FLBPluginRegister and stopped in FLBPluginUnregister.
var telemetrySrv *telemetry.Server

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
		Desc:     "Directory holding the bbolt queue.db file for persistent buffering.",
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
	// Stop any existing server before replacing it (hot-reload path).
	if telemetrySrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetrySrv.Stop(ctx)
	}
	telemetrySrv = telemetry.New(addr, logger)
	if err := telemetrySrv.Start(); err != nil {
		// Start only returns non-nil for unexpected errors; bind failures are
		// swallowed and logged inside Start itself. Continue without telemetry.
		logger.Warn("telemetry server start error", "err", err)
	}

	return output.FLBPluginRegisterWithConfigMap(def, pluginName, "Go OTLP output plugin", pluginConfigMap)
}

//export FLBPluginInit
func FLBPluginInit(plugin unsafe.Pointer) int {
	id := output.FLBPluginConfigKey(plugin, "id")
	if id == "" {
		id = fmt.Sprintf("%d", instanceCount)
		instanceCount++
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
		exp = exporter.NewHTTP(otlpHTTP, headers, timeout, tlsCfg, mp)
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
	if telemetrySrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		telemetrySrv.Stop(ctx)
	}
	output.FLBPluginUnregister(def)
}

func main() {}

func parseCSVSet(s string) map[string]struct{} {
	m := make(map[string]struct{})
	for field := range strings.SplitSeq(s, ",") {
		if f := strings.TrimSpace(field); f != "" {
			m[f] = struct{}{}
		}
	}
	return m
}
