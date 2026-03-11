package logger

import (
	"log/slog"
	"os"
)

// New returns a slog.Logger configured based on the verbose flag.
func New(verbose bool) *slog.Logger {
	level := new(slog.LevelVar)
	if verbose {
		level.Set(slog.LevelDebug)
	} else {
		level.Set(slog.LevelInfo)
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
