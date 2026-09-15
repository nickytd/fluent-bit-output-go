// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/otel/metric"
)

type httpExporter struct {
	endpoint string
	headers  http.Header
	client   *http.Client
}

// NewHTTP returns an Exporter that POSTs OTLP/HTTP protobuf to endpoint+"/v1/logs".
// A trailing slash on endpoint is trimmed, and an endpoint that already ends in
// "/v1/logs" is used as-is, so both "https://host" and "https://host/v1/logs"
// resolve to the same target.
// headers (may be nil) are attached to every request after Content-Type.
// timeout is applied as http.Client.Timeout; zero means no timeout.
// tlsCfg (may be nil) is set on the HTTP transport; nil uses system defaults.
// mp is used to create request/byte/duration instruments; pass a noop provider to disable.
func NewHTTP(endpoint string, headers http.Header, timeout time.Duration, tlsCfg *tls.Config, mp metric.MeterProvider) Exporter {
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &httpExporter{
		endpoint: logsEndpoint(endpoint),
		headers:  headers,
		client: &http.Client{
			Timeout:   timeout,
			Transport: newMetricsRoundTripper(transport, mp),
		},
	}
}

// logsEndpoint normalizes a configured OTLP/HTTP base into the logs URL,
// avoiding a doubled "//v1/logs" from a trailing slash or a repeated
// "/v1/logs" when the caller already included the signal path.
func logsEndpoint(endpoint string) string {
	trimmed := strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(trimmed, "/v1/logs") {
		return trimmed
	}
	return trimmed + "/v1/logs"
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