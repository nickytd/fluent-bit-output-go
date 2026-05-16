package main

import (
	"C"
	"fmt"
	"log/slog"
	"unsafe"

	"github.com/fluent/fluent-bit-go/output"
	"go.opentelemetry.io/collector/pdata/plog"
)

const pluginName = "go-out"

var instanceCount int

type pluginInstance struct {
	id     string
	logger *slog.Logger
}

//export FLBPluginRegister
func FLBPluginRegister(def unsafe.Pointer) int {
	return output.FLBPluginRegister(def, pluginName, "Go output plugin")
}

//export FLBPluginInit
func FLBPluginInit(plugin unsafe.Pointer) int {
	id := output.FLBPluginConfigKey(plugin, "id")
	if id == "" {
		id = fmt.Sprintf("%d", instanceCount)
	}
	instanceCount++

	queueDir := output.FLBPluginConfigKey(plugin, "queue_dir")
	if queueDir == "" {
		queueDir = "/tmp/fluent-bit-pebble"
	}
	if err := initQueue(queueDir); err != nil {
		slog.New(baseHandler).Error("failed to init queue", "err", err)
		return output.FLB_ERROR
	}

	inst := &pluginInstance{
		id:     id,
		logger: slog.New(baseHandler.WithGroup(id)),
	}
	inst.logger.Info("initialized instance")
	output.FLBPluginSetContext(plugin, inst)
	return output.FLB_OK
}

//export FLBPluginFlushCtx
func FLBPluginFlushCtx(ctx, data unsafe.Pointer, length C.int, tag *C.char) int {
	inst := output.FLBPluginGetContext(ctx).(*pluginInstance)
	dec := output.NewDecoder(data, int(length))

	var records []decodedRecord
	for {
		ret, ts, record := output.GetRecord(dec)
		if ret != 0 {
			break
		}
		records = append(records, decodedRecord{Timestamp: ts, Record: record})
	}

	logs := processRecords(records)

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

	if err := enqueue(b); err != nil {
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
	shutdownQueue()
	slog.New(baseHandler).Info("unregistering plugin")
	output.FLBPluginUnregister(def)
}

func main() {}
