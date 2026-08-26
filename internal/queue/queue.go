// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

// Package queue is the on-disk persistent queue that buffers marshalled
// plog.Logs between Fluent Bit's FLBPluginFlushCtx and the OTLP consumer
// goroutine. It uses a single-file bbolt B+ tree with monotonic 8-byte
// big-endian keys, giving lexicographic FIFO ordering. On startup the
// in-memory sequence counter is reseeded from the bucket's last key so a
// restart with un-drained items never overwrites them.
//
// The package intentionally keeps its state at package scope; the plugin's
// cgo entry points are a single-instance surface, and the sync.Once around
// Init makes the queue a process-wide singleton by design.
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

var (
	bucketName = []byte("logs")
	queueOnce  sync.Once
	db         *bolt.DB
	writeSeq   atomic.Uint64
	mu         sync.Mutex
	cond       *sync.Cond
	cancelFn   context.CancelFunc
	queueDone  chan struct{}
)

// Init opens the bbolt file under dir, spawns the consumer goroutine, and
// wires the given Exporter as the drain target. It is safe to call multiple
// times; only the first call takes effect (sync.Once).
func Init(logger *slog.Logger, dir string, exp exporter.Exporter) error {
	var initErr error
	queueOnce.Do(func() {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			initErr = fmt.Errorf("create queue dir: %w", err)
			return
		}
		d, err := bolt.Open(filepath.Join(dir, "queue.db"), 0o600, nil)
		if err != nil {
			initErr = fmt.Errorf("open bbolt: %w", err)
			return
		}
		if err := d.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists(bucketName)
			return err
		}); err != nil {
			_ = d.Close()
			initErr = fmt.Errorf("create bucket: %w", err)
			return
		}
		// Reseed writeSeq from the last key already on disk so a restart with
		// un-drained items does not overwrite them by starting again from seq=1.
		if err := d.View(func(tx *bolt.Tx) error {
			k, _ := tx.Bucket(bucketName).Cursor().Last()
			if len(k) == 8 {
				writeSeq.Store(binary.BigEndian.Uint64(k))
			}
			return nil
		}); err != nil {
			_ = d.Close()
			initErr = fmt.Errorf("seed writeSeq: %w", err)
			return
		}
		db = d
		cond = sync.NewCond(&mu)
		queueDone = make(chan struct{})

		ctx, cancel := context.WithCancel(context.Background())
		cancelFn = cancel
		go runConsumer(ctx, logger, exp, queueDone)
	})
	return initErr
}

// Enqueue writes one marshalled plog.Logs batch to the bbolt bucket under
// the next monotonically-increasing key and signals the consumer.
func Enqueue(data []byte) error {
	seq := writeSeq.Add(1)
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], seq)
	if err := db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketName).Put(key[:], data)
	}); err != nil {
		return fmt.Errorf("bbolt put: %w", err)
	}
	cond.Signal()
	return nil
}

// Shutdown cancels the consumer, waits for it to exit, and closes the
// underlying bbolt DB. Safe to call from a non-init goroutine.
func Shutdown() {
	if cancelFn != nil {
		cancelFn()
	}
	if cond != nil {
		cond.Broadcast()
	}
	if queueDone != nil {
		<-queueDone
	}
	if db != nil {
		_ = db.Close()
	}
}
