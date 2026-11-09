// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const maxHTTPResponseReadBytes = 1 << 20 // 1 MiB cap on OTLP response body

type httpExporter struct {
	endpoint string
	headers  http.Header
	client   *http.Client
	logger   *slog.Logger
	rejected metric.Int64Counter
}

// NewHTTP returns an Exporter that POSTs OTLP/HTTP protobuf to endpoint+"/v1/logs".
// A trailing slash on endpoint is trimmed, and an endpoint that already ends in
// "/v1/logs" is used as-is, so both "https://host" and "https://host/v1/logs"
// resolve to the same target.
// timeout is applied as http.Client.Timeout; zero means no timeout.
// tlsCfg (may be nil) is set on the HTTP transport; nil uses system defaults.
// mp is used to create request/byte/duration instruments; pass a noop provider to disable.
// headers (may be nil) are attached to every request after Content-Type.
// logger records a warning when the backend reports an OTLP partial success.
func NewHTTP(endpoint string, timeout time.Duration, tlsCfg *tls.Config, mp metric.MeterProvider, headers http.Header, logger *slog.Logger) Exporter {
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &httpExporter{
		endpoint: logsEndpoint(endpoint),
		headers:  headers,
		client: &http.Client{
			Timeout:   timeout,
			Transport: newMetricsRoundTripper(transport, mp),
		},
		logger:   logger,
		rejected: newRejectedCounter(mp),
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("otlp http: status %d", resp.StatusCode)
	}

	// The backend has already accepted the records (2xx), so parsing the
	// response body is observability-only: a partial-success payload lets us
	// log/count rejected records, but any trouble reading or decoding it must
	// never turn an accepted export into a failure. Returning an error here
	// would preserve the batch and replay already-delivered records forever
	// (see internal/queue/consumer.go). Read unconditionally — chunked or
	// gunzipped responses report ContentLength == -1, so gating on it would
	// silently skip partial-success reporting.
	reader := http.MaxBytesReader(nil, resp.Body, maxHTTPResponseReadBytes)
	data, err := io.ReadAll(reader)
	if err != nil {
		e.logger.Warn("otlp http: reading response body", "err", err)
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	exportResponse := plogotlp.NewExportResponse()
	if err := exportResponse.UnmarshalProto(data); err != nil {
		e.logger.Warn("otlp http: decoding response body", "err", err)
		return nil
	}
	ps := exportResponse.PartialSuccess()
	if ps.RejectedLogRecords() != 0 || ps.ErrorMessage() != "" {
		e.rejected.Add(ctx, ps.RejectedLogRecords(), metric.WithAttributes(
			attribute.String("transport", "http"),
		))
		e.logger.Warn("otlp http partial success",
			"rejected_log_records", ps.RejectedLogRecords(),
			"message", ps.ErrorMessage(),
		)
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
