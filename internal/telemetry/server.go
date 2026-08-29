// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

// Package telemetry provides the shared observability HTTP endpoint for the
// plugin. A single Server is started at plugin load time (FLBPluginRegister)
// and stopped at unload (FLBPluginUnregister). It exposes:
//
//   - /metrics  — Prometheus text format via the OTel SDK→Prometheus bridge
//   - /debug/pprof/ — Go pprof profiling endpoints
//
// The Server never calls os.Exit or log.Fatal; a bind failure is logged as a
// warning and the plugin continues without observability.
package telemetry

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Server is the shared observability HTTP endpoint. One instance is created at
// plugin load time in FLBPluginRegister and stopped in FLBPluginUnregister.
type Server struct {
	mp     *sdkmetric.MeterProvider
	srv    *http.Server
	addr   string // actual bound address (host:port); empty when disabled
	done   chan struct{}
	logger *slog.Logger
}

// New creates a Server that will listen on addr. Call Start to bind and serve.
// addr is typically read from the FLB_GO_OUT_DEBUG_ADDR env var by the caller.
func New(addr string, logger *slog.Logger) *Server {
	s := &Server{
		done:   make(chan struct{}),
		logger: logger,
	}

	// Private registry — never use prometheus.DefaultRegisterer so the plugin
	// does not pollute or conflict with the host process's global registry.
	registry := prometheus.NewRegistry()

	// The Prometheus exporter implements sdkmetric.Reader as a pull-based
	// reader. When promhttp.HandlerFor triggers a scrape it collects from all
	// registered OTel instruments and translates the OTel data model into
	// Prometheus text format.
	promExp, err := otelprom.New(
		otelprom.WithRegisterer(registry),
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		logger.Warn("telemetry: failed to create prometheus exporter, observability disabled", "err", err)
		return s
	}

	s.mp = sdkmetric.NewMeterProvider(sdkmetric.WithReader(promExp))

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		Registry: registry,
	}))

	// Register pprof endpoints explicitly on the private mux. We use the named
	// import (not blank _) to avoid the DefaultServeMux side-effect.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	s.srv = &http.Server{
		Addr:    addr,
		Handler: mux,
		// gosec G112: 30s is intentionally generous to allow long pprof profile collections.
		ReadHeaderTimeout: 30 * time.Second,
	}
	return s
}

// Start binds the listener and begins serving. A bind failure (e.g. port
// already in use) is logged as a warning and returns nil — the plugin must
// not crash because observability is unavailable.
func (s *Server) Start() error {
	if s.srv == nil {
		// Server was disabled in New (prometheus exporter init failed).
		close(s.done)
		return nil
	}

	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		s.logger.Warn("telemetry: bind failed, observability disabled", "addr", s.srv.Addr, "err", err)
		// Discard the server so MeterProvider returns noop.
		s.srv = nil
		if s.mp != nil {
			_ = s.mp.Shutdown(context.Background())
			s.mp = nil
		}
		close(s.done)
		return nil
	}

	go func() {
		defer close(s.done)
		_ = s.srv.Serve(ln)
	}()
	s.addr = ln.Addr().String()
	return nil
}

// Stop gracefully shuts down the HTTP server and flushes the OTel SDK.
// Safe to call even if Start was never called.
func (s *Server) Stop(ctx context.Context) {
	if s.srv != nil {
		_ = s.srv.Shutdown(ctx)
	}
	// done is always closed: either by Start's goroutine, by the bind-failure
	// path, or by the New-failure path — so this never deadlocks.
	<-s.done
	if s.mp != nil {
		_ = s.mp.Shutdown(ctx)
	}
}

// Addr returns the actual bound address (e.g. "127.0.0.1:2021") after Start.
// Returns an empty string when the server is disabled.
func (s *Server) Addr() string { return s.addr }

// MeterProvider returns the OTel MeterProvider backed by the Prometheus bridge.
// When the server is disabled (bind failed), it returns a no-op provider so
// callers never need nil checks — instruments simply do nothing.
func (s *Server) MeterProvider() metric.MeterProvider {
	if s.mp != nil {
		return s.mp
	}
	return noop.NewMeterProvider()
}
