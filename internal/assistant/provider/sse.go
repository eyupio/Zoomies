// Package provider holds the adapters that speak to a model over HTTP and
// the pieces they share: a server-sent event reader, the stream that turns
// events into the assistant's, and the words a status code gets. Only
// internal/assistant imports it.
package provider

import (
	"bufio"
	"io"
	"iter"
	"strings"
)

// ServerEvent is one event as a server wrote it: its name, when it gave one,
// and its data lines joined with newlines.
type ServerEvent struct {
	Name string
	Data string
}

// maxLine bounds one line of a stream. A delta is a few hundred bytes; a
// megabyte is a server misbehaving, and the reader says so rather than
// growing without bound.
const maxLine = 1 << 20

// ReadEvents yields the events in r until it ends. Comment lines (a leading
// colon) are skipped, which is what keep-alives are; an event the stream
// ends in the middle of is still yielded, because the terminal event of both
// protocols is often the one a server forgets to put a blank line after.
func ReadEvents(r io.Reader) iter.Seq2[ServerEvent, error] {
	return func(yield func(ServerEvent, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), maxLine)
		var cur ServerEvent
		var data []string
		pending := false
		flush := func() bool {
			if !pending {
				return true
			}
			cur.Data = strings.Join(data, "\n")
			ok := yield(cur, nil)
			cur, data, pending = ServerEvent{}, nil, false
			return ok
		}
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if !flush() {
					return
				}
			case strings.HasPrefix(line, ":"):
				// A comment: keep-alives are written this way.
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					cur.Name = value
					pending = true
				case "data":
					data = append(data, value)
					pending = true
				}
			}
		}
		if err := sc.Err(); err != nil {
			yield(ServerEvent{}, err)
			return
		}
		flush()
	}
}
