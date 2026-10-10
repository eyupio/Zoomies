package store

import (
	"log/slog"
	"os"
	"testing"
)

// TestMain raises the default logger to warnings for the whole package. Every
// test opens a database, and opening one logs a line for each of the hundred
// migrations, so a run printed tens of thousands of them and a failure was
// buried so deep in the CI log that the job's own output could not be paged
// back to it. Warnings and errors still print.
func TestMain(m *testing.M) {
	slog.SetLogLoggerLevel(slog.LevelWarn)
	os.Exit(m.Run())
}
