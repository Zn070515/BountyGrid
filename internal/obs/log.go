// Package obs configures structured operational logging. CLI JSON payloads
// stay on stdout; logs go to stderr so pipelines are unaffected.
package obs

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Setup installs a JSON slog handler on w. BOUNTYGRID_LOG selects
// debug|info|warn|error (default warn).
func Setup(w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	level := slog.LevelWarn
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BOUNTYGRID_LOG"))) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger
}
