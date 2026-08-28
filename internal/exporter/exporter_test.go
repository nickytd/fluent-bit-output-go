// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
)

func TestParseHeaders_empty(t *testing.T) {
	h, err := ParseHeaders("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h) != 0 {
		t.Fatalf("expected empty header, got %v", h)
	}
}

func TestParseHeaders_valid(t *testing.T) {
	h, err := ParseHeaders("Authorization=Bearer token123,X-Tenant=acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer token123" {
		t.Errorf("Authorization: got %q, want %q", got, "Bearer token123")
	}
	if got := h.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant: got %q, want %q", got, "acme")
	}
}

func TestParseHeaders_valueWithColon(t *testing.T) {
	// Header values may contain colons (e.g. Bearer tokens).
	h, err := ParseHeaders("Authorization=Bearer foo:bar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := h.Get("Authorization"); got != "Bearer foo:bar" {
		t.Errorf("got %q, want %q", got, "Bearer foo:bar")
	}
}

func TestParseHeaders_canonicalKey(t *testing.T) {
	h, err := ParseHeaders("x-custom-header=val")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := h["X-Custom-Header"]; !ok {
		t.Errorf("expected canonical key X-Custom-Header, got keys: %v", h)
	}
}

func TestParseHeaders_invalid(t *testing.T) {
	cases := []string{
		"NoEqualsSign",
		"=EmptyName",
		"  =SpacedEmptyName",
	}
	for _, raw := range cases {
		_, err := ParseHeaders(raw)
		if err == nil {
			t.Errorf("ParseHeaders(%q): expected error, got nil", raw)
		}
	}
}

func TestHTTPExporterSendsHeaders(t *testing.T) {
	var gotAuth, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Tenant")
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	headers, err := ParseHeaders("Authorization=Bearer secret,X-Tenant=acme")
	if err != nil {
		t.Fatalf("ParseHeaders: %v", err)
	}

	exp := NewHTTP(srv.URL, headers)
	defer func() { _ = exp.Shutdown(context.Background()) }()

	if err := exp.Export(context.Background(), plog.NewLogs()); err != nil {
		t.Fatalf("Export: %v", err)
	}

	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer secret")
	}
	if gotTenant != "acme" {
		t.Errorf("X-Tenant: got %q, want %q", gotTenant, "acme")
	}
}

func TestHTTPExporterNoHeaders(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := NewHTTP(srv.URL, nil)
	defer func() { _ = exp.Shutdown(context.Background()) }()

	if err := exp.Export(context.Background(), plog.NewLogs()); err != nil {
		t.Fatalf("Export: %v", err)
	}

	if gotContentType != "application/x-protobuf" {
		t.Errorf("Content-Type: got %q, want %q", gotContentType, "application/x-protobuf")
	}
}
