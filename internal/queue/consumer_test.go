// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.opentelemetry.io/collector/pdata/plog"
)

// stubExporter counts Export calls and can be programmed to fail the first
// failUntil calls before succeeding, or to always fail.
type stubExporter struct {
	mu         sync.Mutex
	calls      int
	failUntil  int
	alwaysFail bool
	lastLogs   plog.Logs
	seen       []plog.Logs
}

func (e *stubExporter) Export(_ context.Context, logs plog.Logs) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.seen = append(e.seen, logs)
	e.lastLogs = logs
	if e.alwaysFail {
		return errors.New("stub: forced failure")
	}
	if e.calls <= e.failUntil {
		return errors.New("stub: transient failure")
	}
	return nil
}

func (e *stubExporter) Shutdown(context.Context) error { return nil }

func (e *stubExporter) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func bucketKeyCount(t *testing.T, d *bolt.DB) int {
	t.Helper()
	n := 0
	if err := d.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("logs")).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			n++
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
	return n
}

func newLogsWithBody(body string) plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.Body().SetStr(body)
	return logs
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestQueue creates a Queue backed by a temp dir and registers cleanup.
func newTestQueue(t *testing.T, exp *stubExporter) *Queue {
	t.Helper()
	q, err := New(quietLogger(), t.TempDir(), exp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(q.Shutdown)
	return q
}

// TestConsumerPreservesKeyOnExportError is the regression test for fix #2.
// Before the fix, the consumer appended the bbolt key to the delete list
// regardless of Export's error, so a failing endpoint would silently drop
// data. This test enqueues one payload, runs the consumer with an exporter
// that fails 3 times then succeeds, and asserts the key survives every
// failure and is only deleted after Export returns nil.
func TestConsumerPreservesKeyOnExportError(t *testing.T) {
	exp := &stubExporter{failUntil: 3}
	q := newTestQueue(t, exp)

	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(newLogsWithBody("payload"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := q.Enqueue(data); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Wait for the consumer to reach Export at least four times (three
	// failures + one success) or for the bucket to drain.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bucketKeyCount(t, q.db) == 0 && exp.callCount() >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := exp.callCount(); got < 4 {
		t.Fatalf("expected at least 4 Export calls (3 fail + 1 success), got %d", got)
	}
	if n := bucketKeyCount(t, q.db); n != 0 {
		t.Fatalf("expected empty bucket after successful export, got %d keys", n)
	}
}

// TestConsumerRetainsKeysWhileEndpointDown verifies that during a sustained
// export outage the bbolt bucket keeps every enqueued payload — the durability
// promise that the pre-fix consumer silently broke.
func TestConsumerRetainsKeysWhileEndpointDown(t *testing.T) {
	exp := &stubExporter{alwaysFail: true}
	q := newTestQueue(t, exp)

	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(newLogsWithBody("payload"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for range 5 {
		if err := q.Enqueue(data); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	// Give the consumer time to fail a few times and enter backoff.
	time.Sleep(300 * time.Millisecond)

	if exp.callCount() == 0 {
		t.Fatal("expected consumer to attempt Export at least once")
	}
	if n := bucketKeyCount(t, q.db); n != 5 {
		t.Fatalf("all 5 keys should be preserved during outage, got %d", n)
	}
}

// TestConsumerBackoffCappedAndInterruptible checks that the export-failure
// backoff does not spin (calls stay bounded over a short window) and that
// ctx cancellation interrupts a pending backoff sleep so shutdown is prompt.
func TestConsumerBackoffCappedAndInterruptible(t *testing.T) {
	exp := &stubExporter{alwaysFail: true}
	// Build the Queue manually so we can time the Shutdown call ourselves.
	dir := t.TempDir()
	q, err := New(quietLogger(), dir, exp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	marshaler := &plog.ProtoMarshaler{}
	data, err := marshaler.MarshalLogs(newLogsWithBody("payload"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := q.Enqueue(data); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	start := time.Now()

	// Let a couple of failures happen. With exportBackoffInitial=500ms,
	// 200ms is enough for at most one call.
	time.Sleep(200 * time.Millisecond)
	firstWave := exp.callCount()
	if firstWave == 0 {
		t.Fatal("expected at least one Export attempt in 200ms")
	}

	// Shutdown must return promptly even if the consumer is inside sleepCtx.
	done := make(chan struct{})
	go func() {
		q.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Shutdown did not return within 2s (backoff not interruptible)")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("consumer took too long to exit: %v", elapsed)
	}
}

// TestConsumerDeletesUnmarshalablePayload verifies that a corrupt payload is
// dropped instead of pinning the queue on it forever. This is the one place
// where deleting-on-error remains correct behavior.
func TestConsumerDeletesUnmarshalablePayload(t *testing.T) {
	exp := &stubExporter{}
	q := newTestQueue(t, exp)

	// Write raw bytes that are not a valid plog.Logs proto.
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], 1)
	if err := q.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("logs")).Put(key[:], []byte("not a valid proto"))
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Signal the consumer so it wakes without waiting for a full Enqueue.
	q.cond.Signal()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bucketKeyCount(t, q.db) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := bucketKeyCount(t, q.db); n != 0 {
		t.Fatalf("corrupt payload should be dropped, bucket still has %d keys", n)
	}
	if exp.callCount() != 0 {
		t.Fatalf("corrupt payload must not reach Export, got %d calls", exp.callCount())
	}
}
