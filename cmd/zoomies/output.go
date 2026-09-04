package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// The three output modes every read command supports. Tables are for people,
// JSON is for jq, YAML is for a human reading a big nested object.
const (
	outputTable = "table"
	outputJSON  = "json"
	outputYAML  = "yaml"
)

// ANSI attributes. They are only ever emitted when the destination is a
// terminal, so nothing that gets piped into a file or a pipeline carries them.
const (
	colourReset  = "0"
	colourBold   = "1"
	colourDim    = "2"
	colourRed    = "31"
	colourGreen  = "32"
	colourYellow = "33"
	colourBlue   = "34"
	colourCyan   = "36"
)

func colourise(code, s string) string { return "\x1b[" + code + "m" + s + "\x1b[" + colourReset + "m" }

// isTerminal reports whether w is a terminal that wants colour.
//
// NO_COLOR is honoured because a great many people set it, and a tool that
// ignores it is a tool they stop using. Anything that is not an *os.File --
// a pipe, a test buffer -- is not a terminal, which is what makes the table
// tests assert on plain text.
func isTerminal(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// printer renders a command's output in the mode the operator asked for.
type printer struct {
	out    io.Writer
	format string
	colour bool
	// now is the clock relative times are measured against. Tests set it so
	// that "3m ago" is a fact rather than a race.
	now func() time.Time
}

// newPrinter validates the --output value and prepares a renderer for it. cmd
// is the command being run, so that a rejected format names the right --help.
func newPrinter(e *env, cmd, format string) (*printer, error) {
	f := strings.ToLower(strings.TrimSpace(format))
	switch f {
	case "", outputTable:
		f = outputTable
	case outputJSON:
	case outputYAML, "yml":
		f = outputYAML
	default:
		return nil, usagef(cmd, "--output %q is not a format; use table, json or yaml", format)
	}
	return &printer{out: e.out, format: f, colour: isTerminal(e.out), now: time.Now}, nil
}

// structured reports whether the caller should hand over the raw response
// rather than build a table out of it.
func (p *printer) structured() bool { return p.format != outputTable }

// emit writes an API response verbatim in the requested machine format.
//
// The bytes are the server's, not a re-marshalling of a struct this CLI
// happens to know about: a field added to the API tomorrow shows up in
// `--output json` today, rather than being silently dropped by a client that
// has not been rebuilt.
func (p *printer) emit(raw []byte) error {
	switch p.format {
	case outputJSON:
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			// Not JSON at all; pass it through rather than lose it.
			_, err := p.out.Write(raw)
			return err
		}
		buf.WriteByte('\n')
		_, err := p.out.Write(buf.Bytes())
		return err
	case outputYAML:
		out, err := jsonToYAML(raw)
		if err != nil {
			return err
		}
		_, err = p.out.Write(out)
		return err
	default:
		_, err := p.out.Write(raw)
		return err
	}
}

// jsonToYAML re-renders a JSON document as YAML.
//
// Numbers are decoded as json.Number and converted back deliberately: the
// default float64 decoding turns a GitHub run ID into 1.234567891e+09, which is
// not a number anybody can paste anywhere.
func jsonToYAML(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("the controller's answer is not JSON: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(exactNumbers(v)); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func exactNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = exactNumbers(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = exactNumbers(val)
		}
		return t
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	default:
		return v
	}
}

// ---------------------------------------------------------------------------
// Tables
// ---------------------------------------------------------------------------

// table writes aligned columns.
//
// The alignment is computed here rather than with text/tabwriter because cells
// may carry colour: tabwriter counts the escape bytes as width and the columns
// come out ragged exactly when the operator is looking at them.
func (p *printer) table(headers []string, rows [][]string) {
	columns := len(headers)
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	widths := make([]int, columns)
	for i, h := range headers {
		widths[i] = displayWidth(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], displayWidth(cell))
		}
	}

	write := func(cells []string, style func(string) string) {
		var b strings.Builder
		for i, cell := range cells {
			text := cell
			if style != nil {
				text = style(cell)
			}
			if i == len(cells)-1 {
				// No padding on the last column: trailing spaces are what
				// makes copying a line out of a terminal annoying.
				b.WriteString(text)
				break
			}
			b.WriteString(text)
			b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
		}
		fmt.Fprintln(p.out, strings.TrimRight(b.String(), " "))
	}

	if len(headers) > 0 {
		write(headers, func(s string) string {
			s = strings.ToUpper(s)
			if p.colour {
				return colourise(colourDim, s)
			}
			return s
		})
	}
	for _, row := range rows {
		write(row, nil)
	}
}

// note writes a line of prose: what a command says when there is nothing to
// tabulate, which is a sentence and not an empty table.
func (p *printer) note(format string, a ...any) {
	fmt.Fprintf(p.out, format+"\n", a...)
}

// keyValues prints a detail view: one label per line, aligned.
func (p *printer) keyValues(rows [][2]string) {
	width := 0
	for _, r := range rows {
		if n := displayWidth(r[0]); n > width {
			width = n
		}
	}
	for _, r := range rows {
		label := r[0]
		if p.colour {
			label = colourise(colourDim, label)
		}
		fmt.Fprintf(p.out, "%s%s  %s\n", label, strings.Repeat(" ", width-displayWidth(r[0])), r[1])
	}
}

func (p *printer) paint(code, s string) string {
	if !p.colour || s == "" {
		return s
	}
	return colourise(code, s)
}

// state renders a runner or job state in the colour that matches what it means:
// green for working, red for broken, dim for finished.
func (p *printer) state(s string) string {
	switch s {
	case "busy", "in_progress", "success":
		return p.paint(colourGreen, s)
	case "idle", "queued":
		return p.paint(colourCyan, s)
	case "failed", "failure", "cancelled":
		return p.paint(colourRed, s)
	case "draining", "provisioning", "registering":
		return p.paint(colourYellow, s)
	case "removed", "completed", "skipped":
		return p.paint(colourDim, s)
	default:
		return s
	}
}

// yesNo renders a boolean the way a table column reads best, and colours only
// the answer that is worth noticing.
func (p *printer) yesNo(b bool, notableWhen bool) string {
	s := "no"
	if b {
		s = "yes"
	}
	if b == notableWhen {
		return p.paint(colourYellow, s)
	}
	return s
}

// displayWidth counts the columns a string occupies, ignoring ANSI escapes.
func displayWidth(s string) int {
	n, inEscape := 0, false
	for _, r := range s {
		switch {
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		case r == '\x1b':
			inEscape = true
		default:
			n++
		}
	}
	if n == 0 {
		return utf8.RuneCountInString(s)
	}
	return n
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

// dash renders an absent value. An empty cell in a table looks like a bug; a
// dash looks like an answer.
func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// relTime renders a timestamp as an age, which is what an operator scanning a
// list actually reads. Anything older than a season becomes a date, because
// "97d ago" is not something anybody converts in their head.
func (p *printer) relTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := p.now().Sub(t)
	if d < 0 {
		return "in " + compactDuration(-d)
	}
	if d < time.Second {
		return "just now"
	}
	if d > 90*24*time.Hour {
		return t.Local().Format("2006-01-02")
	}
	return compactDuration(d) + " ago"
}

// relTimePtr is relTime for the many nullable timestamps in the API.
func (p *printer) relTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return p.relTime(*t)
}

// compactDuration renders a duration in one unit, because a column is one unit
// wide and "2h13m47s" is not more useful than "2h" when scanning.
func compactDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Round(time.Minute)/time.Minute)) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Round(time.Hour)/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d.Round(24*time.Hour)/(24*time.Hour))) + "d"
	}
}

// millis renders a millisecond count from the API as a duration a person reads.
func millis(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return strconv.FormatInt(ms, 10) + "ms"
	case d < time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	default:
		return compactDuration(d)
	}
}

// truncate keeps a long free-text column from wrapping the whole table. It
// trims to a rune boundary and marks the cut, so nothing looks complete when it
// is not.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:max(n-1, 1)]) + "…"
}

// bar draws a utilisation meter. It is the one piece of ornament in the CLI and
// it earns its place: a column of bars shows an unbalanced fleet at a glance in
// a way a column of percentages does not.
func (p *printer) bar(fraction float64, width int) string {
	if width < 1 {
		width = 1
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction*float64(width) + 0.5)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	switch {
	case fraction >= 0.9:
		return p.paint(colourRed, bar)
	case fraction >= 0.6:
		return p.paint(colourYellow, bar)
	default:
		return p.paint(colourGreen, bar)
	}
}
