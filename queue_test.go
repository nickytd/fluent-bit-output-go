package main

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func TestPebbleQueueRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "test-svc")
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("my-lib")
	lr := sl.LogRecords().AppendEmpty()
	lr.Body().SetStr("hello pebble")
	lr.SetSeverityNumber(plog.SeverityNumberInfo)
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)))
	lr.Attributes().PutStr("env", "test")

	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(logs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var key [8]byte
	binary.BigEndian.PutUint64(key[:], 1)
	if err := d.Set(key[:], data, pebble.Sync); err != nil {
		t.Fatalf("set: %v", err)
	}

	val, closer, err := d.Get(key[:])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = closer.Close() }()

	unmarshaler := &plog.ProtoUnmarshaler{}
	got, err := unmarshaler.UnmarshalLogs(val)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gotLR := got.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	if gotLR.Body().Str() != "hello pebble" {
		t.Fatalf("body mismatch: %s", gotLR.Body().Str())
	}
	if gotLR.SeverityNumber() != plog.SeverityNumberInfo {
		t.Fatalf("severity mismatch: %d", gotLR.SeverityNumber())
	}
	env, _ := gotLR.Attributes().Get("env")
	if env.Str() != "test" {
		t.Fatalf("attr mismatch: %s", env.Str())
	}
}

func TestPebbleQueueOrdering(t *testing.T) {
	dir := t.TempDir()
	d, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()

	for i := range uint64(10) {
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], i+1)
		val := []byte{byte(i + 1)}
		if err := d.Set(key[:], val, pebble.Sync); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}

	iter, err := d.NewIter(nil)
	if err != nil {
		t.Fatalf("new iter: %v", err)
	}
	defer func() { _ = iter.Close() }()

	var order []uint64
	for iter.First(); iter.Valid(); iter.Next() {
		seq := binary.BigEndian.Uint64(iter.Key())
		order = append(order, seq)
	}

	if len(order) != 10 {
		t.Fatalf("expected 10 items, got %d", len(order))
	}
	for i, seq := range order {
		if seq != uint64(i+1) {
			t.Fatalf("order[%d] = %d, want %d", i, seq, i+1)
		}
	}
}

func TestPebbleQueueDeleteAfterRead(t *testing.T) {
	dir := t.TempDir()
	d, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()

	var key [8]byte
	binary.BigEndian.PutUint64(key[:], 1)
	if err := d.Set(key[:], []byte("data"), pebble.Sync); err != nil {
		t.Fatalf("set: %v", err)
	}

	if err := d.Delete(key[:], pebble.Sync); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, _, err = d.Get(key[:])
	if err != pebble.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}

	iter, err := d.NewIter(nil)
	if err != nil {
		t.Fatalf("new iter: %v", err)
	}
	defer func() { _ = iter.Close() }()
	if iter.First() {
		t.Fatal("expected empty iterator after delete")
	}
}

func TestQueueIntegration(t *testing.T) {
	dir := t.TempDir()
	d, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = d.Close() }()

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.Body().SetStr("integration test")
	lr.SetSeverityNumber(plog.SeverityNumberWarn)

	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(logs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for i := range uint64(5) {
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], i+1)
		if err := d.Set(key[:], data, pebble.Sync); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	iter, err := d.NewIter(nil)
	if err != nil {
		t.Fatalf("new iter: %v", err)
	}
	defer func() { _ = iter.Close() }()

	count := 0
	unmarshaler := &plog.ProtoUnmarshaler{}
	for iter.First(); iter.Valid(); iter.Next() {
		got, err := unmarshaler.UnmarshalLogs(iter.Value())
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		gotLR := got.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
		if gotLR.Body().Str() != "integration test" {
			t.Fatalf("body mismatch: %s", gotLR.Body().Str())
		}
		if gotLR.SeverityNumber() != plog.SeverityNumberWarn {
			t.Fatalf("severity mismatch: %d", gotLR.SeverityNumber())
		}
		count++
	}
	if count != 5 {
		t.Fatalf("expected 5 items, got %d", count)
	}
}
