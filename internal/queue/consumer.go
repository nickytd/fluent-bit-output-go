// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"log/slog"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/nickytd/fluent-bit-output-go/internal/exporter"
)

// Bounds for the export-failure backoff. On repeated Export errors the drain
// loop would otherwise spin at full CPU because hasItems() stays true — one
// key surviving the drain is enough to satisfy the wake condition immediately.
const (
	exportBackoffInitial = 500 * time.Millisecond
	exportBackoffMax     = 30 * time.Second
)

func runConsumer(ctx context.Context, q *Queue, logger *slog.Logger, exp exporter.Exporter) {
	defer close(q.done)
	defer func() { _ = exp.Shutdown(ctx) }()

	unmarshaler := &plog.ProtoUnmarshaler{}
	backoff := exportBackoffInitial

	for {
		q.mu.Lock()
		for !q.hasItems() && ctx.Err() == nil {
			q.cond.Wait()
		}
		q.mu.Unlock()

		if ctx.Err() != nil {
			return
		}

		// Track whether any Export in this drain pass failed so we can back
		// off before the next iteration and avoid a spin loop against a
		// persistently-failing endpoint.
		exportFailed := false
		var keys [][]byte
		_ = q.db.View(func(tx *bolt.Tx) error {
			c := tx.Bucket(bucketName).Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				key := make([]byte, len(k))
				copy(key, k)
				data := make([]byte, len(v))
				copy(data, v)

				logs, err := unmarshaler.UnmarshalLogs(data)
				if err != nil {
					// A payload we cannot decode will never succeed on retry,
					// so drop it rather than pin the queue on a poison record.
					logger.Error("consumer: unmarshal error, dropping payload", "err", err)
					keys = append(keys, key)
					continue
				}

				if err := exp.Export(ctx, logs); err != nil {
					logger.Error("consumer: export error, preserving payload for retry", "err", err)
					exportFailed = true
					// Stop draining on the first failure: further keys are
					// almost certainly going to hit the same error, and
					// deleting later successes while leaving earlier failures
					// would reorder retries.
					return nil
				}
				keys = append(keys, key)
			}
			return nil
		})

		if len(keys) > 0 {
			_ = q.db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket(bucketName)
				for _, k := range keys {
					_ = b.Delete(k)
				}
				return nil
			})
		}

		if exportFailed {
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, exportBackoffMax)
		} else {
			backoff = exportBackoffInitial
		}
	}
}

// sleepCtx blocks for d or until ctx is cancelled. Returns false if ctx was
// cancelled, so the caller can exit its loop promptly on shutdown.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (q *Queue) hasItems() bool {
	var hasItems bool
	_ = q.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketName).Cursor()
		k, _ := c.First()
		hasItems = k != nil
		return nil
	})
	return hasItems
}
