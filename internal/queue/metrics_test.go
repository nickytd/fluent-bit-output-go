// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// depthValue collects metrics from the reader and returns the number of
// flbgoout.queue.depth datapoints and their summed value. The persistent
// MeterProvider outlives individual queues, so this is how we assert stale
// depth-gauge callbacks are gone after Shutdown.
func depthValue(t *testing.T, reader sdkmetric.Reader) (points int, sum int64) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "flbgoout.queue.depth" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("depth is not Gauge[int64], got %T", m.Data)
			}
			for _, dp := range g.DataPoints {
				points++
				sum += dp.Value
			}
		}
	}
	return points, sum
}

func enqueueN(t *testing.T, q *Queue, n int) {
	t.Helper()
	data, err := (&plog.ProtoMarshaler{}).MarshalLogs(newLogsWithBody("depth"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for range n {
		if err := q.Enqueue(data); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	// Let the consumer make one (failing) drain attempt so items stay queued.
	time.Sleep(100 * time.Millisecond)
}

// TestDepthCallbackUnregisteredOnShutdown verifies the depth-gauge callback is
// removed from the shared meter when the queue shuts down, so it no longer
// observes a dead queue on subsequent scrapes.
func TestDepthCallbackUnregisteredOnShutdown(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	q, err := NewWithID(quietLogger(), t.TempDir(), "q1", &stubExporter{alwaysFail: true}, mp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	enqueueN(t, q, 3)

	if pts, sum := depthValue(t, reader); pts != 1 || sum != 3 {
		t.Fatalf("before shutdown: expected 1 point summing to 3, got points=%d sum=%d", pts, sum)
	}

	q.Shutdown()

	if pts, _ := depthValue(t, reader); pts != 0 {
		t.Fatalf("after shutdown: expected the depth callback gone (0 points), got %d", pts)
	}
}

// TestDepthNoDoubleCountAfterReload simulates a hot-reload on the persistent
// provider: an old queue is shut down and a new queue for the same instance id
// is created on the SAME MeterProvider. Only the live queue's callback must
// observe — exactly one datapoint, not two.
func TestDepthNoDoubleCountAfterReload(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	q1, err := NewWithID(quietLogger(), t.TempDir(), "x", &stubExporter{alwaysFail: true}, mp)
	if err != nil {
		t.Fatalf("New q1: %v", err)
	}
	enqueueN(t, q1, 2)
	q1.Shutdown()

	q2, err := NewWithID(quietLogger(), t.TempDir(), "x", &stubExporter{alwaysFail: true}, mp)
	if err != nil {
		t.Fatalf("New q2: %v", err)
	}
	t.Cleanup(q2.Shutdown)
	enqueueN(t, q2, 5)

	pts, sum := depthValue(t, reader)
	if pts != 1 {
		t.Fatalf("expected exactly 1 depth datapoint after reload, got %d (sum=%d)", pts, sum)
	}
	if sum != 5 {
		t.Fatalf("expected depth 5 from live queue, got %d", sum)
	}
}
