package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

var baseHandler slog.Handler

func init() {
	baseHandler = &flbHandler{w: os.Stderr}
}

var _ slog.Handler = (*flbHandler)(nil)

type flbHandler struct {
	w      io.Writer
	groups []string
	attrs  []slog.Attr
}

func (h *flbHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *flbHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Format("2006/01/02 15:04:05.000")
	level := strings.ToLower(r.Level.String())

	tag := pluginName
	if len(h.groups) > 0 {
		tag = pluginName + ":" + strings.Join(h.groups, ":")
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
		groups: h.groups,
		attrs:  append(append([]slog.Attr{}, h.attrs...), attrs...),
	}
}

func (h *flbHandler) WithGroup(name string) slog.Handler {
	return &flbHandler{
		w:      h.w,
		groups: append(append([]string{}, h.groups...), name),
		attrs:  h.attrs,
	}
}

func writeAttr(buf *strings.Builder, a slog.Attr) {
	fmt.Fprintf(buf, " %s=%s", a.Key, a.Value.String())
}
