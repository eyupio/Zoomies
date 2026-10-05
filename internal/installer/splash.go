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

// Splash is a short illuminated Zoomies banner. It finishes
// before deployment output or prompts begin, so it cannot erase a finding or
// consume an answer. Redirected output never receives animation controls.
func Splash(ctx context.Context, out io.Writer, action string) error {
	ui := PaletteFor(out)
	if !splashAllowed(ui) {
		return nil
	}
	if f, ok := out.(*os.File); ok {
		if width, height, err := term.GetSize(int(f.Fd())); err != nil || width < 45 || height < splashLines+2 {
			return nil
		}
	}
	return animateSplash(ctx, out, ui, action, 40*time.Millisecond)
}

func splashAllowed(ui Palette) bool {
	if !ui.On || os.Getenv("TERM") == "dumb" || os.Getenv("CI") != "" {
		return false
	}
	return strings.TrimSpace(os.Getenv("ZOOMIES_NO_ANIMATION")) == ""
}

func animateSplash(ctx context.Context, out io.Writer, ui Palette, action string, interval time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reserve only the splash's lines; redraws never touch previous output.
	if _, err := fmt.Fprint(out, strings.Repeat("\n", splashLines-1)); err != nil {
		return err
	}
	defer fmt.Fprint(out, splashClear)
	for step := range splashFrames {
		if _, err := fmt.Fprint(out, fmt.Sprintf("\r\x1b[%dA", splashLines-1), splashFrame(step, ui, action)); err != nil {
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

const (
	splashLines  = 7
	splashFrames = 80 // Reveal, highlight sweep, then a one-second hold.
)

var splashClear = "\r\x1b[2K" + strings.Repeat("\x1b[1A\r\x1b[2K", splashLines-1)

// Five-row block lettering keeps the banner readable at ordinary terminal
// sizes. Each block occupies one terminal column.
var splashLetters = map[rune][5]string{
	'Z': {"█████", "   █ ", "  █  ", " █   ", "█████"},
	'O': {" ███ ", "█   █", "█   █", "█   █", " ███ "},
	'M': {"█   █", "██ ██", "█ █ █", "█   █", "█   █"},
	'I': {"█████", "  █  ", "  █  ", "  █  ", "█████"},
	'E': {"█████", "█    ", "████ ", "█    ", "█████"},
	'S': {" ████", "█    ", " ███ ", "    █", "████ "},
}

func splashFrame(step int, ui Palette, action string) string {
	lines := make([]string, 0, splashLines)
	for row := range 5 {
		var parts []string
		for _, letter := range "ZOOMIES" {
			parts = append(parts, splashLetters[letter][row])
		}
		var line strings.Builder
		line.WriteString("  ")
		for x, cell := range []rune(strings.Join(parts, " ")) {
			if cell == ' ' {
				line.WriteByte(' ')
			} else {
				line.WriteString(ui.paint(splashColour(step*40, x, row), string(cell)))
			}
		}
		lines = append(lines, line.String())
	}
	lines = append(lines, "", "  "+ui.Dim("Ready. Set. "+action+"."))
	return "\x1b[2K" + strings.Join(lines, "\n\r\x1b[2K")
}

// Use exact brand colours on true-colour terminals, with 256- and 16-colour
// fallbacks. The sweep advances by columns, so there is no layout movement.
func splashColour(ms, x, row int) string {
	colour := 0
	if x >= 14 {
		colour = 1
	}
	if x >= 28 {
		colour = 2
	}
	if ms < 1050 && x*18 > ms-150 {
		colour = 3
	}
	if ms >= 1100 && ms < 2200 {
		distance := float64(x) - (float64(ms-1100)/19 - float64(row)*0.7)
		if distance < 0 {
			distance = -distance
		}
		if distance < 4.8 {
			colour = 4
		}
		if distance < 2.1 {
			colour = 5
		}
	}
	trueColours := [...]string{"38;2;47;128;237", "38;2;34;184;237", "38;2;34;211;238", "38;2;35;48;62", "38;2;142;234;250", "38;2;240;252;255"}
	colours256 := [...]string{"38;5;33", "38;5;39", "38;5;45", "38;5;238", "38;5;123", "38;5;195"}
	colours16 := [...]string{"34", "36", "96", "90", "96", "97"}
	if ct := os.Getenv("COLORTERM"); ct == "truecolor" || ct == "24bit" {
		return trueColours[colour]
	}
	if strings.Contains(os.Getenv("TERM"), "256color") {
		return colours256[colour]
	}
	return colours16[colour]
}
