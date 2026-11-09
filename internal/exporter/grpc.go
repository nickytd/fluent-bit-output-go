// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	grpcotel "google.golang.org/grpc/stats/opentelemetry"
)

type grpcExporter struct {
	client   plogotlp.GRPCClient
	conn     *grpc.ClientConn
	timeout  time.Duration
	logger   *slog.Logger
	rejected metric.Int64Counter
}

// NewGRPC returns an Exporter that sends via plogotlp.GRPCClient.
// timeout is applied per Export call via context.WithTimeout; zero means no timeout.
// tlsCfg (may be nil) selects transport credentials: nil → insecure, non-nil → TLS.
// mp is used to register gRPC client metrics via stats/opentelemetry; pass a noop provider to disable.
// logger records a warning when the backend reports an OTLP partial success.
func NewGRPC(endpoint string, timeout time.Duration, tlsCfg *tls.Config, mp metric.MeterProvider, logger *slog.Logger) (Exporter, error) {
	creds := insecure.NewCredentials()
	if tlsCfg != nil {
		creds = credentials.NewTLS(tlsCfg)
	}
	otelOpt := grpcotel.DialOption(grpcotel.Options{
		MetricsOptions: grpcotel.MetricsOptions{
			MeterProvider: mp,
		},
	})
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds), otelOpt)
	if err != nil {
		return nil, fmt.Errorf("grpc dial: %w", err)
	}
	return &grpcExporter{
		client:   plogotlp.NewGRPCClient(conn),
		conn:     conn,
		timeout:  timeout,
		logger:   logger,
		rejected: newRejectedCounter(mp),
	}, nil
}

func (e *grpcExporter) Export(ctx context.Context, logs plog.Logs) error {
	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}
	req := plogotlp.NewExportRequestFromLogs(logs)
	resp, err := e.client.Export(ctx, req)
	if err != nil {
		return fmt.Errorf("grpc export: %w", err)
	}
	ps := resp.PartialSuccess()
	if ps.RejectedLogRecords() != 0 || ps.ErrorMessage() != "" {
		e.rejected.Add(ctx, ps.RejectedLogRecords(), metric.WithAttributes(
			attribute.String("transport", "grpc"),
		))
		e.logger.Warn("otlp grpc partial success",
			"rejected_log_records", ps.RejectedLogRecords(),
			"message", ps.ErrorMessage(),
		)
	}
	return nil
}

func (e *grpcExporter) Shutdown(_ context.Context) error {
	return e.conn.Close()
}
