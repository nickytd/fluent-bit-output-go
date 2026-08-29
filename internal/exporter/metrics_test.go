// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestMeterProvider returns a fresh in-memory MeterProvider and a collect
// function that gathers all metrics synchronously.
func newTestMP(t *testing.T) (*sdkmetric.MeterProvider, func() metricdata.ResourceMetrics) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	collect := func() metricdata.ResourceMetrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect: %v", err)
		}
		return rm
	}
	return mp, collect
}

func findCounter(rm metricdata.ResourceMetrics, name string) (int64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				if d, ok := m.Data.(metricdata.Sum[int64]); ok {
					var total int64
					for _, dp := range d.DataPoints {
						total += dp.Value
					}
					return total, true
				}
			}
		}
	}
	return 0, false
}

func findCounterByAttr(rm metricdata.ResourceMetrics, name, attrKey, attrVal string) (int64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if d, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range d.DataPoints {
					v, _ := dp.Attributes.Value(attribute.Key(attrKey))
					if v.AsString() == attrVal {
						return dp.Value, true
					}
				}
			}
		}
	}
	return 0, false
}

func findHistogram(rm metricdata.ResourceMetrics, name string) (uint64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				if d, ok := m.Data.(metricdata.Histogram[float64]); ok {
					var total uint64
					for _, dp := range d.DataPoints {
						total += dp.Count
					}
					return total, true
				}
			}
		}
	}
	return 0, false
}

// stubTransport is an http.RoundTripper for testing the metricsRoundTripper.
type stubTransport struct {
	err  error
	code int
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.code,
		Body:       io.NopCloser(nil),
	}, nil
}

func TestMetricsRoundTripperNoopProvider(t *testing.T) {
	// With a noop provider the RoundTripper must not panic and must still
	// delegate to the inner transport.
	inner := &stubTransport{code: http.StatusOK}
	rt := newMetricsRoundTripper(inner, noop.NewMeterProvider())

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMetricsRoundTripperSuccessCounter(t *testing.T) {
	mp, collect := newTestMP(t)
	inner := &stubTransport{code: http.StatusOK}
	rt := newMetricsRoundTripper(inner, mp)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", nil)
	_, _ = rt.RoundTrip(req)

	rm := collect()
	n, ok := findCounterByAttr(rm, "flbgoout.http.requests", "status", "success")
	if !ok {
		t.Fatal("flbgoout.http.requests counter not found")
	}
	if n != 1 {
		t.Fatalf("expected 1 success, got %d", n)
	}
	fails, _ := findCounterByAttr(rm, "flbgoout.http.requests", "status", "failure")
	if fails != 0 {
		t.Fatalf("expected 0 failures, got %d", fails)
	}
}

func TestMetricsRoundTripperFailureCounter(t *testing.T) {
	mp, collect := newTestMP(t)
	inner := &stubTransport{err: errors.New("connection refused")}
	rt := newMetricsRoundTripper(inner, mp)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", nil)
	_, _ = rt.RoundTrip(req)

	rm := collect()
	n, ok := findCounterByAttr(rm, "flbgoout.http.requests", "status", "failure")
	if !ok {
		t.Fatal("flbgoout.http.requests counter not found")
	}
	if n != 1 {
		t.Fatalf("expected 1 failure, got %d", n)
	}
}

func TestMetricsRoundTripperBytesCounter(t *testing.T) {
	mp, collect := newTestMP(t)
	inner := &stubTransport{code: http.StatusOK}
	rt := newMetricsRoundTripper(inner, mp)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", nil)
	req.ContentLength = 128
	_, _ = rt.RoundTrip(req)

	rm := collect()
	n, ok := findCounter(rm, "flbgoout.http.bytes_sent")
	if !ok {
		t.Fatal("flbgoout.http.bytes_sent counter not found")
	}
	if n != 128 {
		t.Fatalf("expected 128 bytes, got %d", n)
	}
}

func TestMetricsRoundTripperDurationObserved(t *testing.T) {
	mp, collect := newTestMP(t)
	inner := &stubTransport{code: http.StatusOK}
	rt := newMetricsRoundTripper(inner, mp)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", nil)
	_, _ = rt.RoundTrip(req)

	rm := collect()
	count, ok := findHistogram(rm, "flbgoout.http.duration")
	if !ok {
		t.Fatal("flbgoout.http.duration histogram not found")
	}
	if count == 0 {
		t.Fatal("expected at least one histogram observation")
	}
}
