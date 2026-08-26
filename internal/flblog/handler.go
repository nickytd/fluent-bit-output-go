// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

// Package flblog provides an slog.Handler that formats records in the same
// [ts] [level] [tag] message style that Fluent Bit prints its own logs in,
// so plugin-side logs blend into the fluent-bit console output.
package flblog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewHandler returns an slog.Handler that writes fluent-bit-formatted lines
// to w, tagged with the given plugin tag. WithGroup nested calls extend the
// tag with ":group1:group2" suffixes.
func NewHandler(w io.Writer, tag string) slog.Handler {
	return &flbHandler{w: w, tag: tag}
}

// NewStderrHandler is a convenience for the common case: writes to os.Stderr
// with the given tag.
func NewStderrHandler(tag string) slog.Handler {
	return NewHandler(os.Stderr, tag)
}

var _ slog.Handler = (*flbHandler)(nil)

type flbHandler struct {
	w      io.Writer
	tag    string
	groups []string
	attrs  []slog.Attr
}

func (h *flbHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *flbHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Format("2006/01/02 15:04:05.000")
	level := strings.ToLower(r.Level.String())

	tag := h.tag
	if len(h.groups) > 0 {
		tag = h.tag + ":" + strings.Join(h.groups, ":")
	}

	var buf strings.Builder
	for _, a := range h.attrs {
		writeAttr(&buf, a)
	}

	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&buf, a)
		return true
	})

	_, err := fmt.Fprintf(h.w, "[%s] [%5s] [%s] %s%s\n", ts, level, tag, r.Message, buf.String())

	return err
}

func (h *flbHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &flbHandler{
		w:      h.w,
		tag:    h.tag,
		groups: h.groups,
		attrs:  append(append([]slog.Attr{}, h.attrs...), attrs...),
	}
}

func (h *flbHandler) WithGroup(name string) slog.Handler {
	return &flbHandler{
		w:      h.w,
		tag:    h.tag,
		groups: append(append([]string{}, h.groups...), name),
		attrs:  h.attrs,
	}
}

func writeAttr(buf *strings.Builder, a slog.Attr) {
	fmt.Fprintf(buf, " %s=%s", a.Key, a.Value.String())
}
