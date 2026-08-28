// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

// Package exporter carries the OTLP exporter implementations used by the
// plugin's consumer goroutine. Three concrete backends are provided:
// stdout (OTLP JSON, for debugging), OTLP/HTTP (protobuf POST), and
// OTLP/gRPC (via plogotlp.GRPCClient).
package exporter

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Exporter is the sink for marshalled log batches drained from the queue.
// Implementations must be safe for concurrent Shutdown but Export is only
// called from a single consumer goroutine.
type Exporter interface {
	Export(ctx context.Context, logs plog.Logs) error
	Shutdown(ctx context.Context) error
}

type stdoutExporter struct {
	m plog.JSONMarshaler
}

// NewStdout returns an Exporter that prints OTLP JSON to stdout. Useful for
// local debugging when no collector is available.
func NewStdout() Exporter {
	return &stdoutExporter{}
}

func (e *stdoutExporter) Export(_ context.Context, logs plog.Logs) error {
	b, err := e.m.MarshalLogs(logs)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = fmt.Fprintln(os.Stdout, string(b))
	return err
}

func (e *stdoutExporter) Shutdown(_ context.Context) error {
	return nil
}

type httpExporter struct {
	endpoint string
	headers  http.Header
	client   *http.Client
}

// NewHTTP returns an Exporter that POSTs OTLP/HTTP protobuf to endpoint+"/v1/logs".
// headers (may be nil) are attached to every request after Content-Type.
// timeout is applied as http.Client.Timeout; zero means no timeout.
// tlsCfg (may be nil) is set on the HTTP transport; nil uses system defaults.
func NewHTTP(endpoint string, headers http.Header, timeout time.Duration, tlsCfg *tls.Config) Exporter {
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &httpExporter{
		endpoint: endpoint + "/v1/logs",
		headers:  headers,
		client:   &http.Client{Timeout: timeout, Transport: transport},
	}
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

// ParseHeaders parses a semicolon-separated list of "Name=Value" pairs into an
// http.Header. An empty string returns an empty header without error. Header
// names are canonicalised via http.CanonicalHeaderKey. Semicolons are used as
// the delimiter (not commas) so that header values containing commas — such as
// "VL-Stream-Fields=host.name,severity" — are parsed correctly.
func ParseHeaders(raw string) (http.Header, error) {
	if raw == "" {
		return http.Header{}, nil
	}
	h := http.Header{}
	for token := range strings.SplitSeq(raw, ";") {
		token = strings.TrimSpace(token)
		name, value, ok := strings.Cut(token, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid header %q: expected Name=Value", token)
		}
		h.Set(strings.TrimSpace(name), value)
	}
	return h, nil
}

func (e *httpExporter) Export(ctx context.Context, logs plog.Logs) error {
	req := plogotlp.NewExportRequestFromLogs(logs)
	body, err := req.MarshalProto()
	if err != nil {
		return fmt.Errorf("marshal proto: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	maps.Copy(httpReq.Header, e.headers)

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("otlp http: status %d", resp.StatusCode)
	}
	return nil
}

func (e *httpExporter) Shutdown(_ context.Context) error {
	e.client.CloseIdleConnections()
	return nil
}

type grpcExporter struct {
	client  plogotlp.GRPCClient
	conn    *grpc.ClientConn
	timeout time.Duration
}

// NewGRPC returns an Exporter that sends via plogotlp.GRPCClient.
// timeout is applied per Export call via context.WithTimeout; zero means no timeout.
// tlsCfg (may be nil) selects transport credentials: nil → insecure, non-nil → TLS.
func NewGRPC(endpoint string, timeout time.Duration, tlsCfg *tls.Config) (Exporter, error) {
	creds := insecure.NewCredentials()
	if tlsCfg != nil {
		creds = credentials.NewTLS(tlsCfg)
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc dial: %w", err)
	}
	return &grpcExporter{
		client:  plogotlp.NewGRPCClient(conn),
		conn:    conn,
		timeout: timeout,
	}, nil
}

func (e *grpcExporter) Export(ctx context.Context, logs plog.Logs) error {
	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}
	req := plogotlp.NewExportRequestFromLogs(logs)
	_, err := e.client.Export(ctx, req)
	if err != nil {
		return fmt.Errorf("grpc export: %w", err)
	}
	return nil
}

func (e *grpcExporter) Shutdown(_ context.Context) error {
	return e.conn.Close()
}
