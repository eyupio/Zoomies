//go:build !windows

package main

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

func TestSIGHUPRereadsTheLogLevel(t *testing.T) {
	path := writeConfig(t, "log:\n  level: debug\n")
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	apply := func(l string) string {
		previous := level.Level().String()
		level.Set(config.ParseLogLevel(l))
		return previous
	}
	stop := watchSIGHUP(ctx, path, apply, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("sending SIGHUP: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for level.Level() != slog.LevelDebug {
		if time.Now().After(deadline) {
			t.Fatalf("the level is still %s after SIGHUP", level.Level())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
