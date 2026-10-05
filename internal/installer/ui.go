package installer

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// Palette styles the upgrade's terminal output. It is off for anything that is
// not a terminal, and when NO_COLOR is set, so a log file or a pipeline gets
// plain lines with ASCII markers and nothing else.
type Palette struct{ On bool }

// PaletteFor decides from where the output is going.
func PaletteFor(w io.Writer) Palette {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return Palette{}
	}
	f, ok := w.(*os.File)
	return Palette{On: ok && term.IsTerminal(int(f.Fd()))}
}

func (p Palette) paint(code, s string) string {
	if !p.On || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p Palette) Bold(s string) string   { return p.paint("1", s) }
func (p Palette) Dim(s string) string    { return p.paint("2", s) }
func (p Palette) Accent(s string) string { return p.paint("1;36", s) }
func (p Palette) Green(s string) string  { return p.paint("32", s) }
func (p Palette) Yellow(s string) string { return p.paint("33", s) }
func (p Palette) Red(s string) string    { return p.paint("31", s) }

// mark returns a status glyph: a real one on a terminal, ASCII elsewhere.
func (p Palette) mark(glyph, ascii string) string {
	if p.On {
		return glyph
	}
	return ascii
}

// Title is the heading of a run: the name in the accent colour, then what it
// is acting on, dimmed.
func (p Palette) Title(out io.Writer, name, detail string) {
	fmt.Fprintf(out, "%s  %s\n", p.Accent(name), p.Dim(detail))
}

// Done reports a step that finished.
func (p Palette) Done(out io.Writer, format string, a ...any) {
	fmt.Fprintf(out, " %s %s\n", p.Green(p.mark("✓", "ok")), fmt.Sprintf(format, a...))
}

// Doing reports a step that is under way, before it has a result.
func (p Palette) Doing(out io.Writer, format string, a ...any) {
	fmt.Fprintf(out, " %s %s\n", p.Accent(p.mark("›", ">")), fmt.Sprintf(format, a...))
}

// Warn reports something worth reading that did not stop the run.
func (p Palette) Warn(out io.Writer, format string, a ...any) {
	fmt.Fprintf(out, " %s %s\n", p.Yellow(p.mark("!", "!")), fmt.Sprintf(format, a...))
}

// Fail reports a finding that is an error.
func (p Palette) Fail(out io.Writer, format string, a ...any) {
	fmt.Fprintf(out, " %s %s\n", p.Red(p.mark("✗", "x")), fmt.Sprintf(format, a...))
}

// Rule uses a short heading rather than a fixed-width divider, so maintenance
// stages fit a narrow terminal and do not fill redirected logs with decoration.
func (p Palette) Rule(out io.Writer, label string) {
	fmt.Fprintf(out, "\n%s\n", p.Bold(label))
}

// Hint is a dimmed aside, indented under the line it belongs to.
func (p Palette) Hint(out io.Writer, format string, a ...any) {
	fmt.Fprintf(out, "   %s\n", p.Dim(fmt.Sprintf(format, a...)))
}
