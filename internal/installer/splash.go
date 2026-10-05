package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// UpgradeSplash is a short dog sprint beneath the Zoomies wordmark. It finishes
// before deployment output or prompts begin, so it cannot erase a finding or
// consume an answer. Redirected output never receives animation controls.
func UpgradeSplash(ctx context.Context, out io.Writer) error {
	ui := PaletteFor(out)
	if !splashAllowed(ui) {
		return nil
	}
	if f, ok := out.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width < 32 {
			return nil
		}
	}
	return animateUpgradeSplash(ctx, out, ui, 100*time.Millisecond)
}

func splashAllowed(ui Palette) bool {
	if !ui.On || os.Getenv("TERM") == "dumb" {
		return false
	}
	return strings.TrimSpace(os.Getenv("ZOOMIES_NO_ANIMATION")) == ""
}

func animateUpgradeSplash(ctx context.Context, out io.Writer, ui Palette, interval time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reserve three lines and redraw only those lines. No hidden cursor or
	// alternate screen; cleanup returns to the start before reports begin.
	if _, err := fmt.Fprint(out, "\n\n"); err != nil {
		return err
	}
	defer fmt.Fprint(out, splashClear)
	for step := range 21 {
		position := 20 - step
		// The dog spots a squirrel and sprints left. Only the most
		// recent three footprints remain, giving the trail a short tail.
		trail := strings.Repeat("· ", min(step, 3))
		caption := "Squirrel! …upgrade first."
		if step >= 14 {
			caption = "Good dog. Let's upgrade."
		}
		if _, err := fmt.Fprintf(out, "\r\x1b[2A\x1b[2K  %s  %s\n\r\x1b[2K  🐿  %s🐕%s\n\r\x1b[2K  %s",
			ui.Accent("zoomies"), ui.Dim("/ upgrade"), strings.Repeat(" ", position), ui.Dim(trail), ui.Dim(caption)); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// Clear from the caption back through the track and heading, leaving the
// cursor at the start of the original line, without touching earlier output.
const splashClear = "\r\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"
