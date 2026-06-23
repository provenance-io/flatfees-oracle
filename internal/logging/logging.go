// Package logging configures structured JSON logging to stdout. Routing to
// Kibana/Datadog/Slack is handled downstream by the cluster log shipper and
// Datadog; this package only ensures clean, leveled, consistently-tagged events.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a slog.Logger emitting JSON to stdout at the given level, tagged
// with the service name and environment so downstream filtering is trivial.
func New(level, env string) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(h).With(
		slog.String("service", "flatfees-oracle"),
		slog.String("env", env),
	)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
