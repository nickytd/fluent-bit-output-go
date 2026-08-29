// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

// Package queue is the on-disk persistent queue that buffers marshalled
// plog.Logs between Fluent Bit's FLBPluginFlushCtx and the OTLP consumer
// goroutine. It uses a single-file bbolt B+ tree with monotonic 8-byte
// big-endian keys, giving lexicographic FIFO ordering. On startup the
// in-memory sequence counter is reseeded from the bucket's last key so a
// restart with un-drained items never overwrites them.
//
// Each plugin output instance owns its own Queue so that per-instance config
// (queue_dir, otlp_http_headers, exporter) is fully isolated.
package queue

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	bolt "go.etcd.io/bbolt"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/nickytd/fluent-bit-output-go/internal/exporter"
)

var bucketName = []byte("logs")

// Queue is the per-instance persistent FIFO backed by a bbolt file. Create one
// with New; call Shutdown when the plugin instance exits.
type Queue struct {
	db         *bolt.DB
	writeSeq   atomic.Uint64
	mu         sync.Mutex
	cond       *sync.Cond
	cancelFn   context.CancelFunc
	done       chan struct{}
	closed     atomic.Bool // set true in Shutdown before db.Close; guards Depth()
	instanceID string

	// OTel instruments — created from the MeterProvider passed to New.
	enqueued   metric.Int64Counter
	exportOK   metric.Int64Counter
	exportFail metric.Int64Counter
}

// New opens the bbolt file under dir, spawns the consumer goroutine, and wires
// exp as the drain target. mp is used to register queue depth and throughput
// metrics; pass noop.NewMeterProvider() to disable instrumentation.
func New(logger *slog.Logger, dir string, exp exporter.Exporter, mp metric.MeterProvider) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create queue dir: %w", err)
	}
	d, err := bolt.Open(filepath.Join(dir, "queue.db"), 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt: %w", err)
	}
	if err := d.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketName)
		return err
	}); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("create bucket: %w", err)
	}

	q := &Queue{
		db:   d,
		done: make(chan struct{}),
	}
	q.cond = sync.NewCond(&q.mu)

	// Reseed writeSeq from the last key already on disk so a restart with
	// un-drained items does not overwrite them by starting again from seq=1.
	if err := d.View(func(tx *bolt.Tx) error {
		k, _ := tx.Bucket(bucketName).Cursor().Last()
		if len(k) == 8 {
			q.writeSeq.Store(binary.BigEndian.Uint64(k))
		}
		return nil
	}); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("seed writeSeq: %w", err)
	}

	q.initMetrics(mp, logger)

	ctx, cancel := context.WithCancel(context.Background())
	q.cancelFn = cancel
	go runConsumer(ctx, q, logger, exp)

	return q, nil
}

// initMetrics registers OTel instruments and the observable depth gauge.
// Errors from instrument creation are non-fatal — the queue works without them.
func (q *Queue) initMetrics(mp metric.MeterProvider, logger *slog.Logger) {
	meter := mp.Meter("flbgoout/queue")

	attrs := metric.WithAttributes(attribute.String("instance", q.instanceID))

	var err error
	q.enqueued, err = meter.Int64Counter(
		"flbgoout.queue.enqueued",
		metric.WithDescription("Total log batches written to the bbolt queue."),
		metric.WithUnit("{batch}"),
	)
	if err != nil {
		logger.Warn("queue: failed to create enqueued counter", "err", err)
	}

	q.exportOK, err = meter.Int64Counter(
		"flbgoout.queue.exports",
		metric.WithDescription("Total queue drain attempts by outcome."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		logger.Warn("queue: failed to create export counter", "err", err)
	}
	q.exportFail = q.exportOK // same instrument, different attribute value

	// Register an observable gauge that reads the live bucket key count on
	// each scrape. The closure captures q; Depth() guards against closed DB.
	_, err = meter.Int64ObservableGauge(
		"flbgoout.queue.depth",
		metric.WithDescription("Current number of un-drained log batches in the bbolt queue."),
		metric.WithUnit("{batch}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(q.Depth(), attrs)
			return nil
		}),
	)
	if err != nil {
		logger.Warn("queue: failed to register depth gauge", "err", err)
	}
}

// Enqueue writes one marshalled plog.Logs batch to the bbolt bucket under the
// next monotonically-increasing key and signals the consumer.
func (q *Queue) Enqueue(data []byte) error {
	seq := q.writeSeq.Add(1)
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], seq)
	if err := q.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketName).Put(key[:], data)
	}); err != nil {
		return fmt.Errorf("bbolt put: %w", err)
	}
	if q.enqueued != nil {
		q.enqueued.Add(context.Background(), 1,
			metric.WithAttributes(attribute.String("instance", q.instanceID)),
		)
	}
	q.cond.Signal()
	return nil
}

// Depth returns the number of keys currently in the bbolt bucket. It runs a
// read-only bbolt View on every call — cheap but not free; only call from the
// Prometheus scrape path or tests. Returns 0 when the DB is closed.
func (q *Queue) Depth() int64 {
	if q.closed.Load() {
		return 0
	}
	var n int64
	_ = q.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketName).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			n++
		}
		return nil
	})
	return n
}

// Shutdown cancels the consumer, waits for it to exit, and closes the bbolt DB.
func (q *Queue) Shutdown() {
	q.cancelFn()
	q.cond.Broadcast()
	<-q.done
	q.closed.Store(true)
	_ = q.db.Close()
}
