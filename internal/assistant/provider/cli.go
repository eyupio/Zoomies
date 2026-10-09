package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/assistant"
)

// This file is what the providers that run a vendor's own command line tool on
// the controller's machine have in common: Claude Code, Codex and Copilot. Each
// of them is somebody's own subscription, signed in through the vendor's flow, so
// none of them is given a key or an address, and each is run the same careful way:
// in an empty directory, with an environment of its own and in a process group
// that is ended with it. What differs is the arguments, what the tool prints and
// how it says it is signed in, which is each adapter's own file.

// cliSlots bounds how many copies of any tool run at once. Each is a process of its
// own that can hold a few hundred megabytes, and the questions of one controller
// are few; the rest wait their turn.
var cliSlots = make(chan struct{}, 2)

// cliEnvBase is the part of this process's environment every tool is given: where
// it lives, where its home is, and how it reaches the network. Nothing of the
// controller's, and in particular no ZOOMIES_ variable and no token of any vendor.
var cliEnvBase = []string{
	"HOME", "PATH", "USER", "LOGNAME", "LANG", "LC_ALL", "TZ", "TMPDIR",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy",
}

// cliEnv is environ narrowed to cliEnvBase and the names the tool itself adds, which
// are where its own sign-in is kept.
func cliEnv(environ []string, extra ...string) []string {
	keep := make(map[string]bool, len(cliEnvBase)+len(extra))
	for _, k := range cliEnvBase {
		keep[k] = true
	}
	for _, k := range extra {
		keep[k] = true
	}
	var out []string
	for _, kv := range environ {
		if k, _, ok := strings.Cut(kv, "="); ok && keep[k] {
			out = append(out, kv)
		}
	}
	return out
}

// cliBinary finds the executable: the one named, or one on the PATH, or one where
// its installers put it. notFound is what the person is told when there is none.
func cliBinary(override, name string, candidates []string, notFound error) (string, error) {
	if override != "" {
		return override, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return c, nil
		}
	}
	return "", notFound
}

// cliReader turns what a tool prints into events and hands each to emit, which
// says false once nobody is reading any more. answered is whether the tool said
// anything as an answer; finished is whether a last event (the end, or a failure)
// has been handed over already, so that what is left is the process leaving.
type cliReader func(out io.Reader, emit func(assistant.Event) bool) (answered, finished bool)

// cliLines is a tool that prints one JSON object a line.
type cliLines interface {
	line(raw []byte) []assistant.Event
	answered() bool
}

// readLines is a cliReader for a tool that prints lines. A line it does not
// understand is the parser's to ignore: a newer tool may print more than this knows.
func readLines(p cliLines) cliReader {
	return func(out io.Reader, emit func(assistant.Event) bool) (bool, bool) {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			for _, ev := range p.line(sc.Bytes()) {
				if !emit(ev) || ev.Done || ev.Err != nil {
					return p.answered(), true
				}
			}
		}
		return p.answered(), false
	}
}

// readText is a cliReader for a tool that prints its answer as plain text, which
// is passed on as it comes.
func readText(out io.Reader, emit func(assistant.Event) bool) (bool, bool) {
	var answered bool
	buf := make([]byte, 4<<10)
	var carry []byte
	for {
		n, err := out.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			// Never cut a character in two: what is left of one waits for the rest.
			cut := completePrefix(chunk)
			carry = append([]byte(nil), chunk[cut:]...)
			if text := string(chunk[:cut]); text != "" {
				if strings.TrimSpace(text) != "" {
					answered = true
				}
				if !emit(assistant.Event{Delta: text}) {
					return answered, true
				}
			}
		}
		if err != nil {
			if len(carry) > 0 {
				answered = answered || strings.TrimSpace(string(carry)) != ""
				if !emit(assistant.Event{Delta: strings.ToValidUTF8(string(carry), "")}) {
					return answered, true
				}
			}
			return answered, false
		}
	}
}

// completePrefix is how many leading bytes of b are whole characters: a character
// cut by the end of a read has its start at the end of b and the rest still to come.
func completePrefix(b []byte) int {
	for i := 1; i <= 3 && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if utf8.FullRune(b[len(b)-i:]) {
				return len(b)
			}
			return len(b) - i
		}
	}
	return len(b)
}

// cliCommand is one question to a tool.
type cliCommand struct {
	// tool is its name as the person knows it, for the messages.
	tool string
	bin  string
	args []string
	env  []string
	// stdin is what is piped to it, and may be empty.
	stdin  string
	reader cliReader
	// exit is what a run that ended badly with nothing said as an answer means.
	exit func(err error, stderr string) error
}

// startCLI runs the tool and returns the answer as it arrives. It waits for one of
// the slots, and gives it back when the stream is closed.
func startCLI(ctx context.Context, c cliCommand) (assistant.Stream, error) {
	select {
	case cliSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "zoomies-eli-")
	if err != nil {
		<-cliSlots
		return nil, fmt.Errorf("making a directory for %s to run in: %w", c.tool, err)
	}
	cmd := exec.CommandContext(ctx, c.bin, c.args...)
	cmd.Dir = dir
	cmd.Env = c.env
	if c.stdin != "" {
		cmd.Stdin = strings.NewReader(c.stdin)
	}
	cmd.WaitDelay = 3 * time.Second
	ownGroup(cmd)
	stderr := &limitedBuffer{limit: 8 << 10}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		<-cliSlots
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		<-cliSlots
		return nil, fmt.Errorf("starting %s: %w", c.tool, err)
	}
	s := &cliStream{cmd: cmd, dir: dir, events: make(chan assistant.Event, 16), done: make(chan struct{}), quit: make(chan struct{})}
	go s.read(c, out, stderr)
	return s, nil
}

// limitedBuffer keeps the first limit bytes written to it and drops the rest.
type limitedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// cliStream is a question in progress: the process, and what it has said.
type cliStream struct {
	cmd    *exec.Cmd
	dir    string
	events chan assistant.Event
	done   chan struct{}
	quit   chan struct{}
	once   sync.Once
}

// send hands an event to whoever is reading, unless they have closed the stream:
// a reader that stopped must not leave the process waiting for a place to put
// what it says.
func (s *cliStream) send(ev assistant.Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.quit:
		return false
	}
}

// Next implements assistant.Stream.
func (s *cliStream) Next(ctx context.Context) (assistant.Event, bool) {
	select {
	case ev, ok := <-s.events:
		return ev, ok
	case <-ctx.Done():
		return assistant.Event{}, false
	}
}

// Close implements assistant.Stream: it ends the process if it is still running,
// waits for it, and gives the directory and the slot back.
func (s *cliStream) Close() error {
	s.once.Do(func() {
		close(s.quit)
		_ = killGroup(s.cmd)
		<-s.done
		_ = os.RemoveAll(s.dir)
		<-cliSlots
	})
	return nil
}

// read turns what the tool prints into events, and says how it ended.
func (s *cliStream) read(c cliCommand, out io.Reader, stderr *limitedBuffer) {
	defer close(s.done)
	defer close(s.events)
	answered, finished := c.reader(out, s.send)
	if finished {
		// The answer is over, or nobody is listening; what is left is the process
		// leaving, and it is not waited for.
		_ = killGroup(s.cmd)
		_ = s.cmd.Wait()
		return
	}
	err := s.cmd.Wait()
	switch {
	case answered:
		s.send(assistant.Event{Done: true})
	case err != nil:
		s.send(assistant.Event{Err: c.exit(err, stderr.String())})
	default:
		s.send(assistant.Event{Err: errors.New("the run ended in " + c.tool + " without an answer")})
	}
}

// clip shortens s to n bytes for a message, without cutting a character in two.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}
