package main

import (
	"context"
	"log/slog"
	"slices"

	"github.com/cockroachdb/pebble"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

func runConsumer(ctx context.Context, done chan struct{}) {
	defer close(done)

	exporter, err := stdoutlog.New()
	if err != nil {
		slog.New(baseHandler).Error("consumer: failed to create exporter", "err", err)
		return
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)),
	)
	defer func() { _ = provider.Shutdown(context.Background()) }()

	logger := provider.Logger("fluent-bit-go-out")
	unmarshaler := &plog.ProtoUnmarshaler{}

	for {
		mu.Lock()
		for !dbHasItems() && ctx.Err() == nil {
			cond.Wait()
		}
		mu.Unlock()

		if ctx.Err() != nil {
			return
		}

		iter, err := db.NewIter(nil)
		if err != nil {
			slog.New(baseHandler).Error("consumer: new iter error", "err", err)
			return
		}

		for iter.First(); iter.Valid(); iter.Next() {
			key := slices.Clone(iter.Key())
			data := slices.Clone(iter.Value())

			logs, err := unmarshaler.UnmarshalLogs(data)
			if err != nil {
				slog.New(baseHandler).Error("consumer: unmarshal error", "err", err)
				_ = db.Delete(key, pebble.NoSync)
				continue
			}

			emitLogs(ctx, logger, logs)
			_ = db.Delete(key, pebble.NoSync)
		}
		_ = iter.Close()
	}
}

func dbHasItems() bool {
	iter, err := db.NewIter(nil)
	if err != nil {
		return false
	}
	defer func() { _ = iter.Close() }()
	return iter.First()
}

func emitLogs(ctx context.Context, logger otellog.Logger, logs plog.Logs) {
	for i := range logs.ResourceLogs().Len() {
		rl := logs.ResourceLogs().At(i)
		resAttrs := convertMap(rl.Resource().Attributes(), "resource.")

		for j := range rl.ScopeLogs().Len() {
			sl := rl.ScopeLogs().At(j)
			var scopeAttrs []otellog.KeyValue
			if sl.Scope().Name() != "" {
				scopeAttrs = append(scopeAttrs, otellog.String("scope.name", sl.Scope().Name()))
			}
			if sl.Scope().Version() != "" {
				scopeAttrs = append(scopeAttrs, otellog.String("scope.version", sl.Scope().Version()))
			}

			for k := range sl.LogRecords().Len() {
				lr := sl.LogRecords().At(k)
				emitRecord(ctx, logger, lr, resAttrs, scopeAttrs)
			}
		}
	}
}

func emitRecord(ctx context.Context, logger otellog.Logger, lr plog.LogRecord, resAttrs, scopeAttrs []otellog.KeyValue) {
	var rec otellog.Record
	rec.SetTimestamp(lr.Timestamp().AsTime())
	rec.SetObservedTimestamp(lr.ObservedTimestamp().AsTime())
	rec.SetSeverity(otellog.Severity(lr.SeverityNumber()))
	rec.SetSeverityText(lr.SeverityText())
	rec.SetBody(convertValue(lr.Body()))

	var attrs []otellog.KeyValue
	attrs = append(attrs, resAttrs...)
	attrs = append(attrs, scopeAttrs...)
	lr.Attributes().Range(func(k string, v pcommon.Value) bool {
		attrs = append(attrs, otellog.KeyValue{Key: k, Value: convertValue(v)})
		return true
	})
	rec.AddAttributes(attrs...)

	emitCtx := ctx
	tid := lr.TraceID()
	sid := lr.SpanID()
	if !tid.IsEmpty() || !sid.IsEmpty() {
		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID(tid),
			SpanID:  trace.SpanID(sid),
		})
		emitCtx = trace.ContextWithSpanContext(ctx, sc)
	}

	logger.Emit(emitCtx, rec)
}

func convertValue(v pcommon.Value) otellog.Value {
	switch v.Type() {
	case pcommon.ValueTypeStr:
		return otellog.StringValue(v.Str())
	case pcommon.ValueTypeInt:
		return otellog.Int64Value(v.Int())
	case pcommon.ValueTypeDouble:
		return otellog.Float64Value(v.Double())
	case pcommon.ValueTypeBool:
		return otellog.BoolValue(v.Bool())
	case pcommon.ValueTypeBytes:
		return otellog.BytesValue(v.Bytes().AsRaw())
	case pcommon.ValueTypeMap:
		return otellog.MapValue(convertMapKVs(v.Map())...)
	case pcommon.ValueTypeSlice:
		vals := make([]otellog.Value, v.Slice().Len())
		for i := range v.Slice().Len() {
			vals[i] = convertValue(v.Slice().At(i))
		}
		return otellog.SliceValue(vals...)
	default:
		return otellog.StringValue(v.AsString())
	}
}

func convertMap(m pcommon.Map, prefix string) []otellog.KeyValue {
	var kvs []otellog.KeyValue
	m.Range(func(k string, v pcommon.Value) bool {
		kvs = append(kvs, otellog.KeyValue{Key: prefix + k, Value: convertValue(v)})
		return true
	})
	return kvs
}

func convertMapKVs(m pcommon.Map) []otellog.KeyValue {
	var kvs []otellog.KeyValue
	m.Range(func(k string, v pcommon.Value) bool {
		kvs = append(kvs, otellog.KeyValue{Key: k, Value: convertValue(v)})
		return true
	})
	return kvs
}
