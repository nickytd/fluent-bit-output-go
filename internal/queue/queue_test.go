// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"encoding/binary"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/otel/metric/noop"
)

func openTestDB(t *testing.T) *bolt.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := bolt.Open(filepath.Join(dir, "test.db"), 0o600, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("logs"))
		return err
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return d
}

func TestBboltQueueRoundTrip(t *testing.T) {
	d := openTestDB(t)

	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.name", "test-svc")
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("my-lib")
	lr := sl.LogRecords().AppendEmpty()
	lr.Body().SetStr("hello bbolt")
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
	if err := d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("logs")).Put(key[:], data)
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	var val []byte
	if err := d.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("logs")).Get(key[:])
		val = make([]byte, len(v))
		copy(val, v)
		return nil
	}); err != nil {
		t.Fatalf("get: %v", err)
	}

	unmarshaler := &plog.ProtoUnmarshaler{}
	got, err := unmarshaler.UnmarshalLogs(val)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gotLR := got.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	if gotLR.Body().Str() != "hello bbolt" {
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

func TestBboltQueueOrdering(t *testing.T) {
	d := openTestDB(t)

	if err := d.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("logs"))
		for i := range uint64(10) {
			var key [8]byte
			binary.BigEndian.PutUint64(key[:], i+1)
			if err := b.Put(key[:], []byte{byte(i + 1)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	var order []uint64
	if err := d.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("logs")).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			order = append(order, binary.BigEndian.Uint64(k))
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
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

func TestBboltQueueDeleteAfterRead(t *testing.T) {
	d := openTestDB(t)

	var key [8]byte
	binary.BigEndian.PutUint64(key[:], 1)

	if err := d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("logs")).Put(key[:], []byte("data"))
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("logs")).Delete(key[:])
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := d.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("logs")).Get(key[:])
		if v != nil {
			t.Fatal("expected nil after delete")
		}
		return nil
	}); err != nil {
		t.Fatalf("get: %v", err)
	}

	if err := d.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("logs")).Cursor()
		k, _ := c.First()
		if k != nil {
			t.Fatal("expected empty bucket after delete")
		}
		return nil
	}); err != nil {
		t.Fatalf("cursor: %v", err)
	}
}

func TestQueueIntegration(t *testing.T) {
	d := openTestDB(t)

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

	if err := d.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("logs"))
		for i := range uint64(5) {
			var key [8]byte
			binary.BigEndian.PutUint64(key[:], i+1)
			if err := b.Put(key[:], data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	count := 0
	unmarshaler := &plog.ProtoUnmarshaler{}
	if err := d.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("logs")).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			got, err := unmarshaler.UnmarshalLogs(v)
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
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}

	if count != 5 {
		t.Fatalf("expected 5 items, got %d", count)
	}
}

// seedWriteSeqFromDB mirrors the reseed step inside New so the restart path
// can be tested without constructing a full Queue.
func seedWriteSeqFromDB(t *testing.T, d *bolt.DB) uint64 {
	t.Helper()
	var seq uint64
	if err := d.View(func(tx *bolt.Tx) error {
		k, _ := tx.Bucket([]byte("logs")).Cursor().Last()
		if len(k) == 8 {
			seq = binary.BigEndian.Uint64(k)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return seq
}

func TestWriteSeqReseedAfterRestart(t *testing.T) {
	d := openTestDB(t)

	// Simulate a pre-restart run that wrote keys 1..5 and crashed before drain.
	if err := d.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("logs"))
		for i := range uint64(5) {
			var key [8]byte
			binary.BigEndian.PutUint64(key[:], i+1)
			if err := b.Put(key[:], []byte{byte(i + 1)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("pre-restart put: %v", err)
	}

	// A naive restart with writeSeq=0 would reuse key=1 on the next enqueue
	// and overwrite the surviving payload. Reseeding from Cursor().Last()
	// must return 5 so the next Add(1) produces 6.
	got := seedWriteSeqFromDB(t, d)
	if got != 5 {
		t.Fatalf("reseed: got %d, want 5", got)
	}

	// Write the next key using the reseeded value and confirm the pre-restart
	// payload at key=1 is untouched.
	nextKey := got + 1
	var kb [8]byte
	binary.BigEndian.PutUint64(kb[:], nextKey)
	if err := d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("logs")).Put(kb[:], []byte("post-restart"))
	}); err != nil {
		t.Fatalf("post-restart put: %v", err)
	}

	var origKey [8]byte
	binary.BigEndian.PutUint64(origKey[:], 1)
	if err := d.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("logs")).Get(origKey[:])
		if len(v) != 1 || v[0] != 1 {
			t.Fatalf("pre-restart payload at key=1 was overwritten: got % x", v)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestWriteSeqReseedOnEmptyBucket(t *testing.T) {
	d := openTestDB(t)
	if got := seedWriteSeqFromDB(t, d); got != 0 {
		t.Fatalf("empty bucket: got %d, want 0", got)
	}
}

// TestNewAfterShutdownReopensDB is the per-instance equivalent of the old
// hot-reload regression test. New must succeed after Shutdown, giving the
// plugin a fresh queue on SIGHUP.
func TestNewAfterShutdownReopensDB(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	exp := &stubExporter{}

	q, err := New(logger, dir, exp, noop.NewMeterProvider())
	if err != nil {
		t.Fatalf("first New: %v", err)
	}

	// Enqueue one payload to confirm the queue is open.
	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(newLogsWithBody("before-reload"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := q.Enqueue(data); err != nil {
		t.Fatalf("Enqueue before reload: %v", err)
	}

	// Simulate FLBPluginExitCtx.
	q.Shutdown()

	// Simulate FLBPluginInit on hot-reload — must re-open the DB.
	q2, err := New(logger, dir, exp, noop.NewMeterProvider())
	if err != nil {
		t.Fatalf("New after Shutdown: %v", err)
	}
	t.Cleanup(q2.Shutdown)

	// Enqueue must succeed with a live DB handle.
	if err := q2.Enqueue(data); err != nil {
		t.Fatalf("Enqueue after hot-reload: %v (database not re-opened)", err)
	}

	// Give the consumer a moment to drain so Shutdown in Cleanup does not race.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if exp.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQueueDepth(t *testing.T) {
	exp := &stubExporter{alwaysFail: true} // keep items in queue
	q := newTestQueue(t, exp)

	if d := q.Depth(); d != 0 {
		t.Fatalf("expected depth 0 on empty queue, got %d", d)
	}

	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(newLogsWithBody("depth-test"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for range 3 {
		if err := q.Enqueue(data); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	// Give the consumer one drain attempt so backoff kicks in and items stay.
	time.Sleep(100 * time.Millisecond)

	if d := q.Depth(); d != 3 {
		t.Fatalf("expected depth 3, got %d", d)
	}
}

func TestQueueDepthZeroAfterShutdown(t *testing.T) {
	exp := &stubExporter{alwaysFail: true}
	dir := t.TempDir()
	q, err := New(quietLogger(), dir, exp, noop.NewMeterProvider())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(newLogsWithBody("shutdown-depth"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := q.Enqueue(data); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	q.Shutdown()

	// After Shutdown the DB is closed; Depth() must return 0, not panic.
	if d := q.Depth(); d != 0 {
		t.Fatalf("expected 0 after Shutdown, got %d", d)
	}
}
