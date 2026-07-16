// Package logging wires slog's default logger to stdout (so debug output
// shows up in `docker logs -f printspy`, same as every other log line) with
// a level that can be flipped at runtime via SetDebug - no restart needed.
// Existing log.Printf call sites are untouched; this only governs the new
// slog.Debug lines added alongside them for verbose tracing.
package logging

import (
	"log/slog"
	"os"
)

var level = new(slog.LevelVar) // defaults to LevelInfo

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

// SetDebug toggles debug-level logging on/off.
func SetDebug(on bool) {
	if on {
		level.Set(slog.LevelDebug)
	} else {
		level.Set(slog.LevelInfo)
	}
}
