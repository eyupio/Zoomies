package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUpgradeSplashLeavesRedirectedOutputUntouched(t *testing.T) {
	var out strings.Builder
	if err := UpgradeSplash(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("animation leaked into a log: %q", out.String())
	}
}

func TestPawTrailPlaysOnceAndClearsItsLine(t *testing.T) {
	var out strings.Builder
	if err := animateUpgradeSplash(context.Background(), &out, Palette{On: true}, 0); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "zoomies") != 8 || strings.Count(text, "🐾") != 8 {
		t.Fatalf("unexpected frames: %q", text)
	}
	if !strings.HasSuffix(text, "\r\x1b[2K") {
		t.Fatal("splash leaves a partial frame on screen")
	}
	if strings.Contains(text, "\n") || strings.Contains(text, "?25") {
		t.Fatal("splash must stay on one line without hiding the cursor")
	}
}

type splashCancelWriter struct {
	strings.Builder
	cancel context.CancelFunc
}

func (w *splashCancelWriter) Write(p []byte) (int, error) { w.cancel(); return w.Builder.Write(p) }

func TestInterruptedSplashClearsTheLineAndStopsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &splashCancelWriter{cancel: cancel}
	if err := animateUpgradeSplash(ctx, out, Palette{On: true}, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if !strings.HasSuffix(out.String(), "\r\x1b[2K") {
		t.Fatal("interrupted frame was not cleared")
	}
}

func TestSplashRespectsTerminalAndMotionPreferences(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("ZOOMIES_NO_ANIMATION", "")
	if !splashAllowed(Palette{On: true}) {
		t.Fatal("normal terminal has no splash")
	}
	if splashAllowed(Palette{}) {
		t.Fatal("plain output animates")
	}
	t.Setenv("TERM", "dumb")
	if splashAllowed(Palette{On: true}) {
		t.Fatal("dumb terminal animates")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("ZOOMIES_NO_ANIMATION", "1")
	if splashAllowed(Palette{On: true}) {
		t.Fatal("motion preference ignored")
	}
}
