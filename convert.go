package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/fluent/fluent-bit-go/output"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

const (
	groupStartTS int64 = -1
	groupEndTS   int64 = -2
)

type decodedRecord struct {
	Timestamp any
	Record    map[any]any
}

func processRecords(records []decodedRecord) plog.Logs {
	logs := plog.NewLogs()

	var currentRL plog.ResourceLogs

	var currentSL plog.ScopeLogs

	inGroup := false

	for _, rec := range records {
		tsUnix := extractUnixSec(rec.Timestamp)

		switch tsUnix {
		case groupStartTS:
			inGroup = true
			currentRL = logs.ResourceLogs().AppendEmpty()
			currentSL = currentRL.ScopeLogs().AppendEmpty()

			// fill in resource/scope logs from the record
			applyGroupBody(rec.Record, currentRL.Resource(), currentSL.Scope())

		case groupEndTS:
			inGroup = false

		default:
			if !inGroup {
				// Flat records get their own ResourceLogs with empty resource/scope.
				currentRL = logs.ResourceLogs().AppendEmpty()
				currentSL = currentRL.ScopeLogs().AppendEmpty()
			}

			lr := currentSL.LogRecords().AppendEmpty()

			// fill in log record fields from the record
			populateLogRecord(lr, rec.Timestamp, rec.Record)
		}
	}

	return logs
}

func extractUnixSec(ts any) int64 {
	var sec uint64

	switch t := ts.(type) {
	case output.FLBTime:
		sec = uint64(t.Unix())
	case uint64:
		sec = t
	default:
		return 0
	}

	switch sec {
	case 0xFFFFFFFF:
		return groupStartTS
	case 0xFFFFFFFE:
		return groupEndTS
	default:
		return int64(sec)
	}
}

func populateLogRecord(lr plog.LogRecord, ts any, record map[any]any) {
	switch t := ts.(type) {
	case output.FLBTime:
		lr.SetTimestamp(pcommon.NewTimestampFromTime(t.Time))
	case uint64:
		lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(int64(t), 0)))
	}

	for k, v := range record {
		key, ok := k.(string)
		if !ok {
			continue
		}

		switch key {
		case "body", "log", "message":
			setBodyValue(lr.Body(), v)
		case "severity_number":
			if n, ok := toInt(v); ok {
				lr.SetSeverityNumber(plog.SeverityNumber(n))
			}
		case "severity_text":
			if s, ok := v.(string); ok {
				lr.SetSeverityText(s)
			}
		case "level":
			if s, ok := v.(string); ok {
				lr.SetSeverityText(s)
				lr.SetSeverityNumber(levelToSeverityNumber(s))
			}
		case "trace_id":
			if s, ok := v.(string); ok {
				lr.SetTraceID(traceIDFromHex(s))
			}
		case "span_id":
			if s, ok := v.(string); ok {
				lr.SetSpanID(spanIDFromHex(s))
			}
		default:
			setAttributeValue(lr.Attributes(), key, v)
		}
	}
}

func applyGroupBody(body map[any]any, resource pcommon.Resource, scope pcommon.InstrumentationScope) {
	if r, ok := body["resource"]; ok {
		if rm, ok := r.(map[any]any); ok {
			if attrs, ok := rm["attributes"]; ok {
				if am, ok := attrs.(map[any]any); ok {
					mapToAttributes(am, resource.Attributes())
				}
			}

			if schemaURL, ok := rm["schema_url"]; ok {
				_ = schemaURL // ResourceLogs.SetSchemaUrl not accessible from Resource
			}
		}
	}

	if s, ok := body["scope"]; ok {
		if sm, ok := s.(map[any]any); ok {
			if name, ok := sm["name"]; ok {
				if n, ok := name.(string); ok {
					scope.SetName(n)
				}
			}

			if version, ok := sm["version"]; ok {
				if v, ok := version.(string); ok {
					scope.SetVersion(v)
				}
			}

			if attrs, ok := sm["attributes"]; ok {
				if am, ok := attrs.(map[any]any); ok {
					mapToAttributes(am, scope.Attributes())
				}
			}
		}
	}
}

func mapToAttributes(m map[any]any, dest pcommon.Map) {
	for k, v := range m {
		key, ok := k.(string)
		if !ok {
			key = fmt.Sprintf("%v", k)
		}

		setAttributeValue(dest, key, v)
	}
}

func setAttributeValue(m pcommon.Map, key string, v any) {
	switch val := v.(type) {
	case string:
		m.PutStr(key, val)
	case []byte:
		m.PutStr(key, string(val))
	case int64:
		m.PutInt(key, val)
	case int:
		m.PutInt(key, int64(val))
	case uint64:
		m.PutInt(key, int64(val))
	case float64:
		m.PutDouble(key, val)
	case bool:
		m.PutBool(key, val)
	default:
		m.PutStr(key, fmt.Sprintf("%v", val))
	}
}

func setBodyValue(dest pcommon.Value, v any) {
	switch val := v.(type) {
	case string:
		dest.SetStr(val)
	case []byte:
		dest.SetStr(string(val))
	case map[any]any:
		m := dest.SetEmptyMap()
		mapToAttributes(val, m)
	default:
		dest.SetStr(fmt.Sprintf("%v", val))
	}
}

func toInt(v any) (int32, bool) {
	switch n := v.(type) {
	case int64:
		return int32(n), true
	case int:
		return int32(n), true
	case uint64:
		return int32(n), true
	default:
		return 0, false
	}
}

func traceIDFromHex(s string) pcommon.TraceID {
	var tid pcommon.TraceID

	if len(s) == 32 {
		for i := range 16 {
			tid[i] = hexToByte(s[i*2], s[i*2+1])
		}
	}

	return tid
}

func spanIDFromHex(s string) pcommon.SpanID {
	var sid pcommon.SpanID

	if len(s) == 16 {
		for i := range 8 {
			sid[i] = hexToByte(s[i*2], s[i*2+1])
		}
	}

	return sid
}

func hexToByte(hi, lo byte) byte {
	return (hexVal(hi) << 4) | hexVal(lo)
}

func hexVal(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10
	default:
		return 0
	}
}

func levelToSeverityNumber(level string) plog.SeverityNumber {
	switch strings.ToLower(level) {
	case "trace":
		return plog.SeverityNumberTrace
	case "debug":
		return plog.SeverityNumberDebug
	case "info":
		return plog.SeverityNumberInfo
	case "warn", "warning":
		return plog.SeverityNumberWarn
	case "error", "err":
		return plog.SeverityNumberError
	case "fatal", "critical", "emerg", "emergency":
		return plog.SeverityNumberFatal
	default:
		return plog.SeverityNumberUnspecified
	}
}
