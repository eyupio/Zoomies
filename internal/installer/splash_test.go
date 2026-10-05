package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUpgradeSplashLeavesRedirectedOutputUntouched(t *testing.T) {
	var out strings.Builder
	if err := Splash(context.Background(), &out, "Upgrade"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("animation leaked into a log: %q", out.String())
	}
}

func TestBannerPlaysOnceAndClearsItsLines(t *testing.T) {
	var out strings.Builder
	if err := animateSplash(context.Background(), &out, Palette{On: true}, "Upgrade", 0); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "Ready. Set. Upgrade.") != splashFrames {
		t.Fatalf("unexpected frames: %q", text)
	}
	if !strings.HasSuffix(text, splashClear) {
		t.Fatal("splash leaves a partial frame on screen")
	}
	if strings.Contains(text, "?25") {
		t.Fatal("splash must not hide the cursor")
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
	if err := animateSplash(ctx, out, Palette{On: true}, "Upgrade", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if !strings.HasSuffix(out.String(), splashClear) {
		t.Fatal("interrupted frame was not cleared")
	}
}

func TestSplashRespectsTerminalAndMotionPreferences(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "")
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

func TestBannerFramesStayInsideReservedTerminalArea(t *testing.T) {
	for step := range splashFrames {
		frame := splashFrame(step, Palette{}, "Upgrade")
		lines := strings.Split(strings.ReplaceAll(frame, "\x1b[2K", ""), "\n\r")
		if len(lines) != splashLines {
			t.Fatalf("frame %d: %d rows", step, len(lines))
		}
		for _, line := range lines {
			if utf8.RuneCountInString(line) >= 45 {
				t.Fatalf("frame %d would wrap: %q", step, line)
			}
		}
	}
}

func TestBannerColourFallbacks(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	if got := splashColour(2500, 0, 0); got != "38;2;47;128;237" {
		t.Fatalf("brand blue = %q", got)
	}
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm-256color")
	if got := splashColour(2500, 0, 0); got != "38;5;33" {
		t.Fatalf("256-colour blue = %q", got)
	}
	t.Setenv("TERM", "xterm")
	if got := splashColour(2500, 0, 0); got != "34" {
		t.Fatalf("16-colour blue = %q", got)
	}
}
