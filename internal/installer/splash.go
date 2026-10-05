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

// UpgradeSplash is a short paw trail beside the Zoomies wordmark. It finishes
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
	return animateUpgradeSplash(ctx, out, ui, 70*time.Millisecond)
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
	// No hidden cursor or alternate screen: even an interrupted splash leaves
	// the terminal ready for the ordinary upgrade report.
	defer fmt.Fprint(out, "\r\x1b[2K")
	for step := range 8 {
		if _, err := fmt.Fprintf(out, "\r\x1b[2K  %s  %s%s", ui.Accent("zoomies"), ui.Dim(strings.Repeat("· ", step)), "🐾"); err != nil {
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
