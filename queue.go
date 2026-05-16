package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
)

var (
	queueOnce sync.Once
	db        *pebble.DB
	writeSeq  atomic.Uint64
	mu        sync.Mutex
	cond      *sync.Cond
	cancelFn  context.CancelFunc
	queueDone chan struct{}
)

func initQueue(dir string, exp exporter) error {
	var initErr error
	queueOnce.Do(func() {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			initErr = fmt.Errorf("create queue dir: %w", err)
			return
		}
		d, err := pebble.Open(dir, &pebble.Options{})
		if err != nil {
			initErr = fmt.Errorf("open pebble: %w", err)
			return
		}
		db = d
		cond = sync.NewCond(&mu)
		queueDone = make(chan struct{})

		ctx, cancel := context.WithCancel(context.Background())
		cancelFn = cancel
		go runConsumer(ctx, exp, queueDone)
	})
	return initErr
}

func enqueue(data []byte) error {
	seq := writeSeq.Add(1)
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], seq)
	if err := db.Set(key[:], data, pebble.NoSync); err != nil {
		return fmt.Errorf("pebble set: %w", err)
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
