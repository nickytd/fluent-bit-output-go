package main

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

func initQueue(logger *slog.Logger, dir string, exp exporter) error {
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
		db = d
		cond = sync.NewCond(&mu)
		queueDone = make(chan struct{})

		ctx, cancel := context.WithCancel(context.Background())
		cancelFn = cancel
		go runConsumer(ctx, logger, exp, queueDone)
	})
	return initErr
}

func enqueue(data []byte) error {
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

func shutdownQueue() {
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
