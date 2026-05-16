package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/cockroachdb/pebble"
	"go.opentelemetry.io/collector/pdata/plog"
)

func runConsumer(ctx context.Context, done chan struct{}) {
	defer close(done)

	unmarshaler := &plog.ProtoUnmarshaler{}
	marshaler := &plog.JSONMarshaler{}

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

			b, err := marshaler.MarshalLogs(logs)
			if err != nil {
				slog.New(baseHandler).Error("consumer: marshal error", "err", err)
				_ = db.Delete(key, pebble.NoSync)
				continue
			}

			fmt.Fprintln(os.Stdout, string(b))
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
