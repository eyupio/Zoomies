// Package logging configures the process-wide structured logger.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Options configures Setup.
type Options struct {
	Level  string // debug | info | warn | error
	Format string // json | text
	// AddSource includes file:line, which is useful in development and noisy
	// in production.
	AddSource bool
}

// Setup builds a logger and installs it as the slog default.
func Setup(o Options) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(o.Level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level:       lvl,
		AddSource:   o.AddSource,
		ReplaceAttr: redact,
	}
	var h slog.Handler
	if strings.ToLower(o.Format) == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}

// sensitiveKeys are attribute names whose values never reach the log, whatever
// a caller passes. It is cheaper to enforce this centrally than to trust every
// call site.
var sensitiveKeys = map[string]bool{
	"password":           true,
	"token":              true,
	"secret":             true,
	"private_key":        true,
	"jit_config":         true,
	"authorization":      true,
	"client_secret":      true,
	"webhook_secret":     true,
	"encryption_key":     true,
	"join_token":         true,
	"agent_token":        true,
	"cookie":             true,
	"registration_token": true,
}

func redact(_ []string, a slog.Attr) slog.Attr {
	if sensitiveKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "[redacted]")
	}
	return a
}

// ctxKey is the context key carrying a request-scoped logger.
type ctxKey struct{}

// WithLogger returns a context carrying l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the request-scoped logger, or the default.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
