// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestNewReloadCounterAccumulates verifies the hot-reload counter created
// against the persistent provider records increments cumulatively. This mirrors
// how FLBPluginRegister creates it once and Adds on each subsequent reload.
func TestNewReloadCounterAccumulates(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	c := newReloadCounter(mp)
	if c == nil {
		t.Fatal("newReloadCounter returned nil for a live provider")
	}
	c.Add(context.Background(), 1)
	c.Add(context.Background(), 1)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var got int64
	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "flbgoout.plugin.reloads" {
				continue
			}
			d, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("reloads is not Sum[int64], got %T", m.Data)
			}
			found = true
			for _, dp := range d.DataPoints {
				got += dp.Value
			}
		}
	}
	if !found {
		t.Fatal("flbgoout.plugin.reloads not found in collected metrics")
	}
	if got != 2 {
		t.Fatalf("expected reload counter 2, got %d", got)
	}
}

// TestNewReloadCounterNoopSafe verifies a noop provider (used when the telemetry
// server is disabled after a bind failure) yields a usable, non-panicking
// counter.
func TestNewReloadCounterNoopSafe(t *testing.T) {
	c := newReloadCounter(noop.NewMeterProvider())
	if c == nil {
		t.Fatal("newReloadCounter returned nil for noop provider")
	}
	c.Add(context.Background(), 1) // must not panic
}
