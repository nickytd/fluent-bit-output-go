// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/collector/pdata/plog"
)

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