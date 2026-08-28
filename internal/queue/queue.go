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

	"github.com/nickytd/fluent-bit-output-go/internal/exporter"
)

var bucketName = []byte("logs")

// Queue is the per-instance persistent FIFO backed by a bbolt file. Create one
// with New; call Shutdown when the plugin instance exits.
type Queue struct {
	db       *bolt.DB
	writeSeq atomic.Uint64
	mu       sync.Mutex
	cond     *sync.Cond
	cancelFn context.CancelFunc
	done     chan struct{}
}

// New opens the bbolt file under dir, spawns the consumer goroutine, and wires
// exp as the drain target.
func New(logger *slog.Logger, dir string, exp exporter.Exporter) (*Queue, error) {
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

	ctx, cancel := context.WithCancel(context.Background())
	q.cancelFn = cancel
	go runConsumer(ctx, q, logger, exp)

	return q, nil
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
	q.cond.Signal()
	return nil
}

// Shutdown cancels the consumer, waits for it to exit, and closes the bbolt DB.
func (q *Queue) Shutdown() {
	q.cancelFn()
	q.cond.Broadcast()
	<-q.done
	_ = q.db.Close()
}
