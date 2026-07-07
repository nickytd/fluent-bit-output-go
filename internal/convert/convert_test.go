package convert

import (
	"testing"
	"time"

	"github.com/fluent/fluent-bit-go/output"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func TestProcessRecords_StandardFlatRecord(t *testing.T) {
	records := []DecodedRecord{
		{
			Timestamp: output.FLBTime{Time: time.Unix(1700000000, 0)},
			Record: map[any]any{
				"message": "hello world",
				"level":   "info",
			},
		},
	}

	logs := ProcessRecords(records)

	if logs.ResourceLogs().Len() != 1 {
		t.Fatalf("expected 1 ResourceLogs, got %d", logs.ResourceLogs().Len())
	}

	rl := logs.ResourceLogs().At(0)
	if rl.Resource().Attributes().Len() != 0 {
		t.Errorf("expected empty resource attributes, got %d", rl.Resource().Attributes().Len())
	}

	sl := rl.ScopeLogs().At(0)
	if sl.LogRecords().Len() != 1 {
		t.Fatalf("expected 1 LogRecord, got %d", sl.LogRecords().Len())
	}

	lr := sl.LogRecords().At(0)
	if lr.Timestamp() != pcommon.NewTimestampFromTime(time.Unix(1700000000, 0)) {
		t.Errorf("unexpected timestamp: %v", lr.Timestamp())
	}

	if lr.Body().Str() != "hello world" {
		t.Errorf("expected body=hello world, got %s", lr.Body().Str())
	}

	if lr.SeverityText() != "info" {
		t.Errorf("expected severity_text=info, got %s", lr.SeverityText())
	}

	if lr.SeverityNumber() != plog.SeverityNumberInfo {
		t.Errorf("expected severity_number=INFO, got %d", lr.SeverityNumber())
	}
}

func TestProcessRecords_OTelFields(t *testing.T) {
	records := []DecodedRecord{
		{
			Timestamp: output.FLBTime{Time: time.Unix(1700000000, 0)},
			Record: map[any]any{
				"body":            "request completed",
				"severity_number": int64(9),
				"severity_text":   "INFO",
				"trace_id":        "0af7651916cd43dd8448eb211c80319c",
				"span_id":         "b7ad6b7169203331",
			},
		},
	}

	logs := ProcessRecords(records)
	lr := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)

	if lr.Body().Str() != "request completed" {
		t.Errorf("expected body=request completed, got %s", lr.Body().Str())
	}

	if lr.SeverityNumber() != plog.SeverityNumberInfo {
		t.Errorf("expected severity 9, got %d", lr.SeverityNumber())
	}

	if lr.SeverityText() != "INFO" {
		t.Errorf("expected severity_text=INFO, got %s", lr.SeverityText())
	}

	expectedTraceID := traceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	if lr.TraceID() != expectedTraceID {
		t.Errorf("trace_id mismatch: got %v", lr.TraceID())
	}

	expectedSpanID := spanIDFromHex("b7ad6b7169203331")
	if lr.SpanID() != expectedSpanID {
		t.Errorf("span_id mismatch: got %v", lr.SpanID())
	}
}

func TestProcessRecords_OTelEnvelopeGroup(t *testing.T) {
	records := []DecodedRecord{
		{
			Timestamp: output.FLBTime{Time: time.Unix(groupStartTS, 0)},
			Record: map[any]any{
				"resource": map[any]any{
					"attributes": map[any]any{
						"service.name": "my-service",
						"host.name":    "node-1",
					},
				},
				"scope": map[any]any{
					"name":    "my-library",
					"version": "1.2.3",
				},
			},
		},
		{
			Timestamp: output.FLBTime{Time: time.Unix(1700000000, 0)},
			Record: map[any]any{
				"body":            "log entry 1",
				"severity_number": int64(9),
			},
		},
		{
			Timestamp: output.FLBTime{Time: time.Unix(1700000001, 0)},
			Record: map[any]any{
				"body":            "log entry 2",
				"severity_number": int64(13),
			},
		},
		{
			Timestamp: output.FLBTime{Time: time.Unix(groupEndTS, 0)},
			Record:    map[any]any{},
		},
	}

	logs := ProcessRecords(records)

	if logs.ResourceLogs().Len() != 1 {
		t.Fatalf("expected 1 ResourceLogs, got %d", logs.ResourceLogs().Len())
	}

	rl := logs.ResourceLogs().At(0)

	svcName, ok := rl.Resource().Attributes().Get("service.name")
	if !ok || svcName.Str() != "my-service" {
		t.Errorf("expected service.name=my-service, got %v", svcName)
	}

	hostName, ok := rl.Resource().Attributes().Get("host.name")
	if !ok || hostName.Str() != "node-1" {
		t.Errorf("expected host.name=node-1, got %v", hostName)
	}

	sl := rl.ScopeLogs().At(0)
	if sl.Scope().Name() != "my-library" {
		t.Errorf("expected scope name=my-library, got %s", sl.Scope().Name())
	}

	if sl.Scope().Version() != "1.2.3" {
		t.Errorf("expected scope version=1.2.3, got %s", sl.Scope().Version())
	}

	if sl.LogRecords().Len() != 2 {
		t.Fatalf("expected 2 LogRecords, got %d", sl.LogRecords().Len())
	}

	lr0 := sl.LogRecords().At(0)
	if lr0.Body().Str() != "log entry 1" {
		t.Errorf("expected body=log entry 1, got %s", lr0.Body().Str())
	}

	if lr0.SeverityNumber() != plog.SeverityNumberInfo {
		t.Errorf("expected severity 9, got %d", lr0.SeverityNumber())
	}

	lr1 := sl.LogRecords().At(1)
	if lr1.Body().Str() != "log entry 2" {
		t.Errorf("expected body=log entry 2, got %s", lr1.Body().Str())
	}

	if lr1.SeverityNumber() != plog.SeverityNumberWarn {
		t.Errorf("expected severity 13, got %d", lr1.SeverityNumber())
	}
}

func TestProcessRecords_MixedBatch(t *testing.T) {
	records := []DecodedRecord{
		{
			Timestamp: output.FLBTime{Time: time.Unix(1700000000, 0)},
			Record:    map[any]any{"message": "flat record"},
		},
		{
			Timestamp: output.FLBTime{Time: time.Unix(groupStartTS, 0)},
			Record: map[any]any{
				"resource": map[any]any{
					"attributes": map[any]any{"service.name": "svc"},
				},
				"scope": map[any]any{"name": "lib"},
			},
		},
		{
			Timestamp: output.FLBTime{Time: time.Unix(1700000001, 0)},
			Record:    map[any]any{"body": "grouped record"},
		},
		{
			Timestamp: output.FLBTime{Time: time.Unix(groupEndTS, 0)},
			Record:    map[any]any{},
		},
		{
			Timestamp: output.FLBTime{Time: time.Unix(1700000002, 0)},
			Record:    map[any]any{"message": "another flat"},
		},
	}

	logs := ProcessRecords(records)

	if logs.ResourceLogs().Len() != 3 {
		t.Fatalf("expected 3 ResourceLogs (flat + grouped + flat), got %d", logs.ResourceLogs().Len())
	}

	// First flat record
	lr0 := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)

	if lr0.Body().Str() != "flat record" {
		t.Errorf("expected first flat record body, got %s", lr0.Body().Str())
	}

	// Grouped record
	rl1 := logs.ResourceLogs().At(1)

	svc, _ := rl1.Resource().Attributes().Get("service.name")
	if svc.Str() != "svc" {
		t.Errorf("expected service.name=svc, got %s", svc.Str())
	}

	lr1 := rl1.ScopeLogs().At(0).LogRecords().At(0)
	if lr1.Body().Str() != "grouped record" {
		t.Errorf("expected grouped record body, got %s", lr1.Body().Str())
	}

	// Second flat record
	lr2 := logs.ResourceLogs().At(2).ScopeLogs().At(0).LogRecords().At(0)

	if lr2.Body().Str() != "another flat" {
		t.Errorf("expected second flat record body, got %s", lr2.Body().Str())
	}
}

func TestProcessRecords_EmptyBatch(t *testing.T) {
	logs := ProcessRecords(nil)
	if logs.ResourceLogs().Len() != 0 {
		t.Errorf("expected 0 ResourceLogs for nil input, got %d", logs.ResourceLogs().Len())
	}
}

func TestTraceIDFromHex(t *testing.T) {
	tests := []struct {
		input    string
		wantZero bool
	}{
		{"0af7651916cd43dd8448eb211c80319c", false},
		{"short", true},
		{"", true},
	}
	for _, tt := range tests {
		tid := traceIDFromHex(tt.input)

		isZero := tid == pcommon.TraceID{}
		if isZero != tt.wantZero {
			t.Errorf("traceIDFromHex(%q): isZero=%v, want %v", tt.input, isZero, tt.wantZero)
		}
	}
}

func TestSpanIDFromHex(t *testing.T) {
	tests := []struct {
		input    string
		wantZero bool
	}{
		{"b7ad6b7169203331", false},
		{"short", true},
		{"", true},
	}
	for _, tt := range tests {
		sid := spanIDFromHex(tt.input)

		isZero := sid == pcommon.SpanID{}
		if isZero != tt.wantZero {
			t.Errorf("spanIDFromHex(%q): isZero=%v, want %v", tt.input, isZero, tt.wantZero)
		}
	}
}

func TestSetAttributeValue(t *testing.T) {
	m := pcommon.NewMap()

	setAttributeValue(m, "str", "hello")
	setAttributeValue(m, "int64", int64(42))
	setAttributeValue(m, "float64", 3.14)
	setAttributeValue(m, "bool", true)
	setAttributeValue(m, "uint64", uint64(99))

	v, ok := m.Get("str")
	if !ok || v.Str() != "hello" {
		t.Errorf("str: got %v", v)
	}

	v, ok = m.Get("int64")
	if !ok || v.Int() != 42 {
		t.Errorf("int64: got %v", v)
	}

	v, ok = m.Get("float64")
	if !ok || v.Double() != 3.14 {
		t.Errorf("float64: got %v", v)
	}

	v, ok = m.Get("bool")
	if !ok || v.Bool() != true {
		t.Errorf("bool: got %v", v)
	}

	v, ok = m.Get("uint64")
	if !ok || v.Int() != 99 {
		t.Errorf("uint64: got %v", v)
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		input any
		want  int32
		ok    bool
	}{
		{int64(9), 9, true},
		{int(13), 13, true},
		{uint64(5), 5, true},
		{"nope", 0, false},
		{nil, 0, false},
	}
	for _, tt := range tests {
		got, ok := toInt(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("toInt(%v): got (%d, %v), want (%d, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}
