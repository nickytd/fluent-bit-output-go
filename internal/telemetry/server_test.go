// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startServer(t *testing.T, addr string) *Server {
	t.Helper()
	srv := New(addr, quietLogger())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Stop(ctx)
	})
	return srv
}

func TestServerStartStop(t *testing.T) {
	srv := startServer(t, ":0")
	if srv.Addr() == "" {
		t.Fatal("Addr() must be non-empty after successful Start")
	}

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected text/plain Content-Type, got %q", ct)
	}
}

func TestServerPprofEndpoint(t *testing.T) {
	srv := startServer(t, ":0")

	resp, err := http.Get("http://" + srv.Addr() + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestServerPortConflict(t *testing.T) {
	first := startServer(t, ":0")
	addr := first.Addr()

	// Second server on the same port — must not error, must be disabled.
	second := New(addr, quietLogger())
	if err := second.Start(); err != nil {
		t.Fatalf("second Start must not error on conflict, got: %v", err)
	}

	// Bind failed: Addr() must be empty, MeterProvider must be non-nil noop.
	if second.Addr() != "" {
		t.Fatalf("expected empty Addr on conflict, got %q", second.Addr())
	}
	if second.MeterProvider() == nil {
		t.Fatal("MeterProvider() must not return nil on bind failure")
	}
}

func TestMetricsCounterVisible(t *testing.T) {
	srv := startServer(t, ":0")

	mp, ok := srv.MeterProvider().(*sdkmetric.MeterProvider)
	if !ok {
		t.Skip("MeterProvider is not SDK — skipping counter visibility test")
	}
	meter := mp.Meter("test")
	counter, err := meter.Int64Counter("test_counter_total")
	if err != nil {
		t.Fatalf("create counter: %v", err)
	}
	counter.Add(context.Background(), 42)

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if !strings.Contains(string(body), "test_counter") {
		t.Fatalf("expected test_counter in /metrics body, got:\n%s", body)
	}
}

func TestGoProcessMetricsExposed(t *testing.T) {
	srv := startServer(t, ":0")

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	for _, want := range []string{"go_goroutines", "go_memstats_alloc_bytes", "process_cpu_seconds_total"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("expected %q in /metrics body", want)
		}
	}
}

func TestMeterProviderNoopOnDisabled(t *testing.T) {
	// A server that never had Start called has nil mp — MeterProvider returns noop.
	srv := &Server{done: make(chan struct{})}
	close(srv.done)
	mp := srv.MeterProvider()
	if mp == nil {
		t.Fatal("MeterProvider must never return nil")
	}
	// Noop provider must not panic on use.
	meter := mp.Meter("test")
	counter, err := meter.Int64Counter("noop_counter")
	if err != nil {
		t.Fatalf("noop counter: %v", err)
	}
	counter.Add(context.Background(), 1)
}
