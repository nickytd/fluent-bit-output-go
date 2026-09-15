// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

// Package exporter carries the OTLP exporter implementations used by the
// plugin's consumer goroutine. Three concrete backends are provided:
// stdout (OTLP JSON, for debugging), OTLP/HTTP (protobuf POST), and
// OTLP/gRPC (via plogotlp.GRPCClient).
package exporter

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
)

// Exporter is the sink for marshalled log batches drained from the queue.
// Implementations must be safe for concurrent Shutdown but Export is only
// called from a single consumer goroutine.
type Exporter interface {
	Export(ctx context.Context, logs plog.Logs) error
	Shutdown(ctx context.Context) error
}

// ParseTimeout parses a duration string (e.g. "10s", "1m") into a time.Duration.
// An empty string returns 0 (no timeout) without error.
func ParseTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: %w", raw, err)
	}
	return d, nil
}