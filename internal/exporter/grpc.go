// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	grpcotel "google.golang.org/grpc/stats/opentelemetry"
)

type grpcExporter struct {
	client  plogotlp.GRPCClient
	conn    *grpc.ClientConn
	timeout time.Duration
}

// NewGRPC returns an Exporter that sends via plogotlp.GRPCClient.
// timeout is applied per Export call via context.WithTimeout; zero means no timeout.
// tlsCfg (may be nil) selects transport credentials: nil → insecure, non-nil → TLS.
// mp is used to register gRPC client metrics via stats/opentelemetry; pass a noop provider to disable.
func NewGRPC(endpoint string, timeout time.Duration, tlsCfg *tls.Config, mp metric.MeterProvider) (Exporter, error) {
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
