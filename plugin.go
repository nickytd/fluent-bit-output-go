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
	"fmt"
	"log/slog"
	"unsafe"

	"github.com/fluent/fluent-bit-go/output"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/nickytd/fluent-bit-output-go/internal/convert"
	"github.com/nickytd/fluent-bit-output-go/internal/exporter"
	"github.com/nickytd/fluent-bit-output-go/internal/flblog"
	"github.com/nickytd/fluent-bit-output-go/internal/queue"
)

const pluginName = "go-out"

// baseHandler is the slog.Handler used for both instance-scoped and
// plugin-scoped log lines. Built once at load time.
var baseHandler = flblog.NewStderrHandler(pluginName)

var instanceCount int

type pluginInstance struct {
	id     string
	logger *slog.Logger
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
}

//export FLBPluginRegister
func FLBPluginRegister(def unsafe.Pointer) int {
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
		id:     id,
		logger: slog.New(baseHandler.WithGroup(id)),
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

	var exp exporter.Exporter
	switch {
	case otlpGRPC != "":
		exp, err = exporter.NewGRPC(otlpGRPC, timeout, tlsCfg)
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
		exp = exporter.NewHTTP(otlpHTTP, headers, timeout, tlsCfg)
	default:
		exp = exporter.NewStdout()
	}

	if err := queue.Init(inst.logger, queueDir, exp); err != nil {
		inst.logger.Error("failed to init queue", "err", err)
		return output.FLB_ERROR
	}

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

	logs := convert.ProcessRecords(records)

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

	if err := queue.Enqueue(b); err != nil {
		inst.logger.Error("enqueue error", "err", err)
		return output.FLB_RETRY
	}
	return output.FLB_OK
}

//export FLBPluginExitCtx
func FLBPluginExitCtx(ctx unsafe.Pointer) int {
	inst := output.FLBPluginGetContext(ctx).(*pluginInstance)
	inst.logger.Info("exiting instance")
	return output.FLB_OK
}

//export FLBPluginUnregister
func FLBPluginUnregister(def unsafe.Pointer) {
	queue.Shutdown()
	slog.New(baseHandler).Info("unregistering plugin")
	output.FLBPluginUnregister(def)
}

func main() {}
