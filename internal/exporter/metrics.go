// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metricsRoundTripper wraps an http.RoundTripper and records per-request
// counters and a latency histogram via the OTel metric API.
type metricsRoundTripper struct {
	inner    http.RoundTripper
	requests metric.Int64Counter
	bytes    metric.Int64Counter
	duration metric.Float64Histogram
}

func newMetricsRoundTripper(inner http.RoundTripper, mp metric.MeterProvider) http.RoundTripper {
	meter := mp.Meter("flbgoout/http")

	requests, _ := meter.Int64Counter(
		"flbgoout.http.requests",
		metric.WithDescription("Total OTLP/HTTP export requests by status."),
		metric.WithUnit("{request}"),
	)
	bytes, _ := meter.Int64Counter(
		"flbgoout.http.bytes_sent",
		metric.WithDescription("Total bytes sent in OTLP/HTTP request bodies."),
		metric.WithUnit("By"),
	)
	duration, _ := meter.Float64Histogram(
		"flbgoout.http.duration",
		metric.WithDescription("OTLP/HTTP export request duration."),
		metric.WithUnit("s"),
	)
	return &metricsRoundTripper{
		inner:    inner,
		requests: requests,
		bytes:    bytes,
		duration: duration,
	}
}

func (rt *metricsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	if req.ContentLength > 0 {
		rt.bytes.Add(req.Context(), req.ContentLength)
	}

	resp, err := rt.inner.RoundTrip(req)

	elapsed := time.Since(start).Seconds()
	rt.duration.Record(req.Context(), elapsed)

	status := "success"
	if err != nil || (resp != nil && resp.StatusCode != http.StatusOK) {
		status = "failure"
	}
	rt.requests.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("status", status),
	))
	return resp, err
}
