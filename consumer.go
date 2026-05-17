package main

import (
	"context"
	"log/slog"

	bolt "go.etcd.io/bbolt"
	"go.opentelemetry.io/collector/pdata/plog"
)

func runConsumer(ctx context.Context, logger *slog.Logger, exp exporter, done chan struct{}) {
	defer close(done)
	defer func() { _ = exp.Shutdown(ctx) }()

	unmarshaler := &plog.ProtoUnmarshaler{}

	for {
		mu.Lock()
		for !dbHasItems() && ctx.Err() == nil {
			cond.Wait()
		}
		mu.Unlock()

		if ctx.Err() != nil {
			return
		}

		var keys [][]byte
		_ = db.View(func(tx *bolt.Tx) error {
			c := tx.Bucket(bucketName).Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				key := make([]byte, len(k))
				copy(key, k)
				data := make([]byte, len(v))
				copy(data, v)

				logs, err := unmarshaler.UnmarshalLogs(data)
				if err != nil {
					logger.Error("consumer: unmarshal error", "err", err)
					keys = append(keys, key)
					continue
				}

				if err := exp.Export(ctx, logs); err != nil {
					logger.Error("consumer: export error", "err", err)
				}
				keys = append(keys, key)
			}
			return nil
		})

		if len(keys) > 0 {
			_ = db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket(bucketName)
				for _, k := range keys {
					_ = b.Delete(k)
				}
				return nil
			})
		}
	}
}

func dbHasItems() bool {
	var hasItems bool
	_ = db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketName).Cursor()
		k, _ := c.First()
		hasItems = k != nil
		return nil
	})
	return hasItems
}
