package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// SanitizingHandler wraps another slog.Handler and sanitizes all attributes.
type SanitizingHandler struct {
	h slog.Handler
}

func NewSanitizingHandler(h slog.Handler) slog.Handler {
	return &SanitizingHandler{h: h}
}

func (sh *SanitizingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return sh.h.Enabled(ctx, level)
}

func (sh *SanitizingHandler) Handle(ctx context.Context, r slog.Record) error {
	// clone the record to avoid mutating the original
	rec := slog.NewRecord(r.Time, r.Level, SanitizeMsg(r.Message), r.PC)

	// sanitize attributes
	r.Attrs(func(a slog.Attr) bool {
		rec.AddAttrs(sanitizeAttr(a))
		return true
	})

	return sh.h.Handle(ctx, rec)
}

func (sh *SanitizingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = sanitizeAttr(a)
	}
	return &SanitizingHandler{h: sh.h.WithAttrs(out)}
}

func (sh *SanitizingHandler) WithGroup(name string) slog.Handler {
	return &SanitizingHandler{h: sh.h.WithGroup(name)}
}

// sanitizeAttr ensures values are cleaned (string or error).
func sanitizeAttr(a slog.Attr) slog.Attr {
	v := a.Value.Any()

	switch x := v.(type) {

	case string:
		return slog.String(a.Key, SanitizeMsg(x))

	case error:
		// Convert error to sanitized string (never return the error type directly)
		return slog.String(a.Key, SanitizeMsg(x.Error()))

	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = SanitizeMsg(s)
		}
		return slog.Any(a.Key, out)

	default:
		return a
	}
}

func SanitizeMsg(msg string) string {
	if privateKeyHex := os.Getenv("PRIVATE_KEY_HEX"); privateKeyHex != "" {
		msg = replaceSecret(msg, privateKeyHex, "<PRIVATE_KEY_HEX>")
	}

	return msg
}

func replaceSecret(msg, secret, placeholder string) string {
	if secret == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, secret, placeholder)
	// If the secret starts with 0x, look for (and replace) all instances of the rest of the secret.
	if raw := strings.TrimPrefix(strings.TrimSpace(secret), "0x"); raw != secret {
		msg = strings.ReplaceAll(msg, raw, placeholder)
	}
	return msg
}
