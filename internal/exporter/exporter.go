// Package exporter carries the OTLP exporter implementations used by the
// plugin's consumer goroutine. Three concrete backends are provided:
// stdout (OTLP JSON, for debugging), OTLP/HTTP (protobuf POST), and
// OTLP/gRPC (via plogotlp.GRPCClient).
package exporter

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
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
	client   *http.Client
}

// NewHTTP returns an Exporter that POSTs OTLP/HTTP protobuf to endpoint+"/v1/logs".
func NewHTTP(endpoint string) Exporter {
	return &httpExporter{
		endpoint: endpoint + "/v1/logs",
		client:   &http.Client{},
	}
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
	client plogotlp.GRPCClient
	conn   *grpc.ClientConn
}

// NewGRPC returns an Exporter that sends via plogotlp.GRPCClient over an
// insecure gRPC channel. TLS/credentials configuration is not yet exposed.
func NewGRPC(endpoint string) (Exporter, error) {
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc dial: %w", err)
	}
	return &grpcExporter{
		client: plogotlp.NewGRPCClient(conn),
		conn:   conn,
	}, nil
}

func (e *grpcExporter) Export(ctx context.Context, logs plog.Logs) error {
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
