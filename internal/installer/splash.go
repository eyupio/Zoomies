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

// UpgradeSplash is a short ASCII dog animation beneath the Zoomies wordmark. It finishes
// before deployment output or prompts begin, so it cannot erase a finding or
// consume an answer. Redirected output never receives animation controls.
func UpgradeSplash(ctx context.Context, out io.Writer) error {
	ui := PaletteFor(out)
	if !splashAllowed(ui) {
		return nil
	}
	if f, ok := out.(*os.File); ok {
		if width, height, err := term.GetSize(int(f.Fd())); err != nil || width < 32 || height < splashLines+2 {
			return nil
		}
	}
	return animateUpgradeSplash(ctx, out, ui, 120*time.Millisecond)
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
	// Reserve only the splash's lines; redraws never touch previous output.
	if _, err := fmt.Fprint(out, strings.Repeat("\n", splashLines-1)); err != nil {
		return err
	}
	defer fmt.Fprint(out, splashClear)
	for step := range 20 {
		if _, err := fmt.Fprint(out, "\r\x1b[9A", splashFrame(step, ui)); err != nil {
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

const splashLines = 10

var splashClear = "\r\x1b[2K" + strings.Repeat("\x1b[1A\r\x1b[2K", splashLines-1)

// Hand-drawn frames retain a recognisable floppy ear and long muzzle while
// the legs cycle, the tail wags and the head sways. ASCII avoids emoji widths
// and different platform glyphs changing the mascot's appearance.
func splashFrame(step int, ui Palette) string {
	legs := []string{` / /  \ \`, ` / |  | \`, ` \ \  / /`, ` | \  / |`}
	dog := []string{
		`     / \__`,
		`    (    o\___`,
		`     /        O`,
		` ___/   (_____/`,
		`/          /`,
		`\___/\____/`,
		legs[step%len(legs)],
	}
	if step%4 >= 2 {
		dog[4] = `~          /`
	}
	if step >= 16 {
		dog[1] = `    (    ^\___`
	}
	caption := "Squirrel detected."
	if step >= 6 {
		caption = "Zoomies engaged."
	}
	if step >= 14 {
		caption = "Good dog. Let's upgrade."
	}
	lines := []string{"  " + ui.Accent("zoomies") + "  " + ui.Dim("/ upgrade"), ""}
	indent := strings.Repeat(" ", 2+min(step/2, 6))
	// A slight head sway gives the gait movement beyond the sliding body.
	for i, line := range dog {
		if step%4 >= 2 && i < 4 {
			line = " " + line
		}
		lines = append(lines, indent+ui.Bold(line))
	}
	lines = append(lines, "  "+ui.Dim(caption))
	return "\x1b[2K" + strings.Join(lines, "\n\r\x1b[2K")
}
