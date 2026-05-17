package main

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

type exporter interface {
	Export(ctx context.Context, logs plog.Logs) error
	Shutdown(ctx context.Context) error
}

type stdoutExporter struct {
	m plog.JSONMarshaler
}

func newStdoutExporter() *stdoutExporter {
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

func newHTTPExporter(endpoint string) *httpExporter {
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

func newGRPCExporter(endpoint string) (*grpcExporter, error) {
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
