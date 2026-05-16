package main

import (
	"context"
	"log/slog"
	"slices"

	"github.com/cockroachdb/pebble"
	"go.opentelemetry.io/collector/pdata/plog"
)

func runConsumer(ctx context.Context, exp exporter, done chan struct{}) {
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

		iter, err := db.NewIter(nil)
		if err != nil {
			slog.New(baseHandler).Error("consumer: new iter error", "err", err)
			return
		}

		for iter.First(); iter.Valid(); iter.Next() {
			key := slices.Clone(iter.Key())
			data := slices.Clone(iter.Value())

			logs, err := unmarshaler.UnmarshalLogs(data)
			if err != nil {
				slog.New(baseHandler).Error("consumer: unmarshal error", "err", err)
				_ = db.Delete(key, pebble.NoSync)
				continue
			}

			if err := exp.Export(ctx, logs); err != nil {
				slog.New(baseHandler).Error("consumer: export error", "err", err)
			}
			_ = db.Delete(key, pebble.NoSync)
		}
		_ = iter.Close()
	}
}

func dbHasItems() bool {
	iter, err := db.NewIter(nil)
	if err != nil {
		return false
	}
	defer func() { _ = iter.Close() }()
	return iter.First()
}
