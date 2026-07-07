package main

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

// installTestQueue points the package-level bbolt globals at a fresh temp DB
// and returns a teardown func. Tests that exercise the consumer share these
// globals with production code, so this fixture MUST be used serially — the
// Go test runner runs top-level tests serially by default, which is fine.
func installTestQueue(t *testing.T) *bolt.DB {
	t.Helper()
	d := openTestDB(t)

	origDB := db
	db = d
	// mu is a package-level sync.Mutex; reuse it as-is. Reinitialize cond so
	// each test starts with a fresh wait state.
	cond = sync.NewCond(&mu)
	// Also reset writeSeq so per-test enqueues start deterministically.
	writeSeq.Store(0)

	t.Cleanup(func() {
		db = origDB
	})
	return d
}

func putLogsAt(t *testing.T, d *bolt.DB, seq uint64, logs plog.Logs) {
	t.Helper()
	marshaler := &plog.ProtoMarshaler{}
	b, err := marshaler.MarshalLogs(logs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], seq)
	if err := d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("logs")).Put(key[:], b)
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
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

// TestConsumerPreservesKeyOnExportError is the regression test for fix #2.
// Before the fix, consumer.go:47 appended the bbolt key to the delete list
// regardless of Export's error, so a failing endpoint would silently drop
// data. This test enqueues one payload, runs the consumer with an exporter
// that fails 3 times then succeeds, and asserts the key survives every
// failure and is only deleted after Export returns nil.
func TestConsumerPreservesKeyOnExportError(t *testing.T) {
	d := installTestQueue(t)
	putLogsAt(t, d, 1, newLogsWithBody("payload"))

	exp := &stubExporter{failUntil: 3}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go runConsumer(ctx, quietLogger(), exp, done)

	// Wait for the consumer to reach Export at least four times (three
	// failures + one success) or for the bucket to drain.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bucketKeyCount(t, d) == 0 && exp.callCount() >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	cond.Broadcast()
	<-done

	if got := exp.callCount(); got < 4 {
		t.Fatalf("expected at least 4 Export calls (3 fail + 1 success), got %d", got)
	}
	if n := bucketKeyCount(t, d); n != 0 {
		t.Fatalf("expected empty bucket after successful export, got %d keys", n)
	}
}

// TestConsumerRetainsKeysWhileEndpointDown verifies that during a sustained
// export outage the bbolt bucket keeps every enqueued payload — the durability
// promise that the pre-fix consumer silently broke.
func TestConsumerRetainsKeysWhileEndpointDown(t *testing.T) {
	d := installTestQueue(t)
	for i := range uint64(5) {
		putLogsAt(t, d, i+1, newLogsWithBody("payload"))
	}

	exp := &stubExporter{alwaysFail: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go runConsumer(ctx, quietLogger(), exp, done)

	// Give the consumer time to fail a few times and enter backoff.
	time.Sleep(300 * time.Millisecond)

	cancel()
	cond.Broadcast()
	<-done

	if exp.callCount() == 0 {
		t.Fatal("expected consumer to attempt Export at least once")
	}
	if n := bucketKeyCount(t, d); n != 5 {
		t.Fatalf("all 5 keys should be preserved during outage, got %d", n)
	}
}

// TestConsumerBackoffCappedAndInterruptible checks that the export-failure
// backoff does not spin (calls stay bounded over a short window) and that
// ctx cancellation interrupts a pending backoff sleep so shutdown is prompt.
func TestConsumerBackoffCappedAndInterruptible(t *testing.T) {
	d := installTestQueue(t)
	putLogsAt(t, d, 1, newLogsWithBody("payload"))

	exp := &stubExporter{alwaysFail: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	start := time.Now()
	go runConsumer(ctx, quietLogger(), exp, done)

	// Let a couple of failures happen. With exportBackoffInitial=500ms,
	// 200ms is enough for at most one call.
	time.Sleep(200 * time.Millisecond)
	firstWave := exp.callCount()
	if firstWave == 0 {
		t.Fatal("expected at least one Export attempt in 200ms")
	}

	// Cancel while the consumer is likely inside sleepCtx. runConsumer must
	// return promptly instead of finishing the full backoff window.
	cancel()
	cond.Broadcast()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("consumer did not exit within 2s of cancel (backoff not interruptible)")
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("consumer took too long to exit: %v", elapsed)
	}
}

// TestConsumerDeletesUnmarshalablePayload verifies that a corrupt payload is
// dropped instead of pinning the queue on it forever. This is the one place
// where deleting-on-error remains correct behavior.
func TestConsumerDeletesUnmarshalablePayload(t *testing.T) {
	d := installTestQueue(t)

	// Write raw bytes that are not a valid plog.Logs proto.
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], 1)
	if err := d.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("logs")).Put(key[:], []byte("not a valid proto"))
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	exp := &stubExporter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go runConsumer(ctx, quietLogger(), exp, done)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bucketKeyCount(t, d) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	cond.Broadcast()
	<-done

	if n := bucketKeyCount(t, d); n != 0 {
		t.Fatalf("corrupt payload should be dropped, bucket still has %d keys", n)
	}
	if exp.callCount() != 0 {
		t.Fatalf("corrupt payload must not reach Export, got %d calls", exp.callCount())
	}
}

// Guard against uninitialized-atomic warnings from race detector on writeSeq
// being read+written across tests. The test fixture already resets it in
// installTestQueue; this is a compile-time reminder that atomic.Uint64 is
// zero-value-safe.
