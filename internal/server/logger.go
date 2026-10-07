package server

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogger builds the process logger from LOG_LEVEL (debug|info|warn|error)
// and LOG_FORMAT (text|json). Unknown values fall back to info / text, so the
// CLI can create a logger before the configuration is validated.
func NewLogger(level, format string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(level)}
	var h slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

// ParseLevel maps a LOG_LEVEL value to a slog level (info when unknown).
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
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
