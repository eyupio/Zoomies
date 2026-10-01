package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// defaultDemoPort is where a controller lives everywhere else in the
// documentation, so an address that is already in somebody's head works for the
// demo too. A machine that has one running on it gets the next free port
// instead of a refusal: the person asking to look around should not have to
// work out what is listening on 8080 first.
const defaultDemoPort = 8080

// demoReadyWithin is how long the demo waits for its own controller to answer
// before it stops saying "starting" and says something is wrong. Seeding the
// fleet and opening the database take about a second; a minute is a machine
// with something badly wrong with it.
const demoReadyWithin = time.Minute

// runDemo is `zoomies demo`: a throwaway controller with a fleet already in it,
// for somebody who wants to see the product before deciding whether to give it
// a machine and a GitHub organisation.
//
// The install is up to eighteen questions and two round trips to github.com
// before the first screen with anything on it, and the populated screen is the
// best argument for the product there is. Until this command the only way to
// reach it was an environment variable mentioned in a migration paragraph.
//
// It is the configuration the browser test suite already runs, and for the same
// reasons: authentication off, which the validator permits only on loopback; no
// agent, so no container runtime is needed; the seeded fleet, whose fixtures
// have no GitHub behind them and never call it; and a database in a directory
// that is removed when the controller stops. Nothing it does outlives it.
func runDemo(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies demo [--port N] [--no-browser]",
		"Run a throwaway controller on this machine with a fleet already in it, to look around.")
	port := fs.Int("port", defaultDemoPort, "port to listen on, on this machine only; if it is taken and was not asked for, a free one is used")
	noBrowser := fs.Bool("no-browser", false, "print the address instead of opening it")
	fs.example(
		"zoomies demo",
		"zoomies demo --port 9000 --no-browser",
		"curl -fsSL https://zoomies.sh/install.sh | sh -s -- --demo     # without installing anything",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if *port < 1 || *port > 65535 {
		return usagef("demo", "--port must be from 1 to 65535, or left out")
	}

	chosen, err := chooseDemoPort(*port, fs.changed("port"), listenLoopback)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(chosen))
	url := "http://" + addr

	dir, err := os.MkdirTemp("", "zoomies-demo-")
	if err != nil {
		return fmt.Errorf("making somewhere to keep the demo's data: %w", err)
	}
	// Removed whichever way this returns, so the demo leaves nothing behind on a
	// clean stop or a failed start. A process killed outright cannot do this, and
	// what it leaves is a directory under the temporary one.
	defer os.RemoveAll(dir)

	// The controller says a good deal at startup -- a banner, and one finding for
	// every setting that is not the safe default, which here is all of them on
	// purpose -- and none of it helps somebody who is only looking. It is kept,
	// and shown only if the start fails, which is when it is the explanation.
	var startup bytes.Buffer
	quiet := &env{out: io.Discard, err: &startup, in: e.in}

	pollCtx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()
	announced := make(chan struct{})
	go func() {
		defer close(announced)
		if err := waitForDemo(pollCtx, url, demoReadyWithin); err != nil {
			return
		}
		announceDemo(e.out, url)
		if !*noBrowser {
			// Best effort, and the address is printed either way: half the
			// machines this runs on have no desktop to open it on.
			_ = openBrowser(pollCtx, url)
		}
	}()

	err = withEnvironment(demoSettings(dir, addr), func() error {
		return runController(ctx, quiet, nil)
	})
	// Waited for, so that nothing is written to the terminal after this
	// returns, and so that what is printed below is not interleaved with the
	// announcement of a demo that was stopped a moment after it came up.
	stopPolling()
	<-announced

	var restart *restartRequested
	switch {
	case err == nil, errors.As(err, &restart):
		// A restart is something only the settings page can ask for, and a
		// throwaway controller has nobody to start the next one.
	default:
		if startup.Len() > 0 {
			fmt.Fprint(e.err, startup.String())
		}
		return err
	}
	// Deleted before it is said to be, not after.
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("the demo stopped, but its data could not be deleted from %s: %w", dir, err)
	}
	fmt.Fprintln(e.out, "\nStopped. The demo's data was deleted.")
	return nil
}

// demoSettings is everything the demo says about its own configuration, as the
// environment layer: the one above the file and the database, and the only one
// that is certain to be read by a controller whose database did not exist a
// moment ago.
func demoSettings(dir, addr string) [][2]string {
	return [][2]string{
		// Loopback, and not a choice: authentication is off, and the validator
		// refuses that on anything a neighbour could reach.
		{"ZOOMIES_BIND", addr},
		{"ZOOMIES_DISABLE_AUTH", "true"},
		{"ZOOMIES_SEED_DEMO", "true"},
		// No agent, so no container runtime is wanted and none is probed.
		{"ZOOMIES_AGENT_EMBEDDED", "false"},
		// The one thing a controller does that is not about its own fleet: it
		// asks github.com, once a day, which release is current. The demo says
		// it talks to nothing outside this machine, so it does not ask. (The
		// fallback poller is left as it is by default: it skips the fixtures,
		// which have no GitHub behind them, and with it on the demo does not
		// open with an error saying nothing is scaling.)
		{"ZOOMIES_UPDATE_CHECK_INTERVAL", "0"},
		{"ZOOMIES_DB_PATH", filepath.Join(dir, "zoomies.db")},
		{"ZOOMIES_STATE_DIR", dir},
		{"ZOOMIES_CONFIG_DIR", dir},
		{"ZOOMIES_WORK_DIR", filepath.Join(dir, "work")},
		{"ZOOMIES_LOG_FORMAT", "text"},
		// Errors only: the seeded hosts have no agent behind them either, and
		// the controller notes that every forty seconds at any lower level.
		{"ZOOMIES_LOG_LEVEL", "error"},
	}
}

// withEnvironment runs fn with the demo's settings and nobody else's.
//
// Every other ZOOMIES_ variable is taken out for the duration and put back
// after. A machine that already runs Zoomies has some of them set -- an
// external URL, an encryption key, a database path -- and any one of them would
// change what the demo is: an external URL alone turns "authentication is off"
// from a warning into a refusal to start, and the person looking around would
// be told to fix a configuration they did not know they had.
func withEnvironment(settings [][2]string, fn func() error) error {
	held := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ZOOMIES_") {
			held[name] = value
			_ = os.Unsetenv(name)
		}
	}
	for _, s := range settings {
		_ = os.Setenv(s[0], s[1])
	}
	defer func() {
		for _, s := range settings {
			_ = os.Unsetenv(s[0])
		}
		for name, value := range held {
			_ = os.Setenv(name, value)
		}
	}()
	return fn()
}

// chooseDemoPort picks the port the demo listens on. One that was asked for is
// used or refused; one that was not is 8080 where that is free and any free
// port where it is not.
func chooseDemoPort(port int, asked bool, listen func(addr string) (net.Listener, error)) (int, error) {
	try := func(port int) (int, bool) {
		l, err := listen(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return 0, false
		}
		defer l.Close()
		if tcp, ok := l.Addr().(*net.TCPAddr); ok {
			return tcp.Port, true
		}
		return port, true
	}
	if got, ok := try(port); ok {
		return got, nil
	}
	if asked {
		return 0, fmt.Errorf("port %d on this machine is already in use; pick another with --port, or leave it out and the demo finds a free one", port)
	}
	if got, ok := try(0); ok {
		return got, nil
	}
	return 0, errors.New("could not find a free port on this machine to listen on")
}

func listenLoopback(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// waitForDemo returns once the controller answers its health check, or says why
// it stopped waiting.
func waitForDemo(ctx context.Context, url string, within time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("the demo did not answer within %s", within)
		case <-tick.C:
		}
	}
}

// announceDemo says where the demo is, and what it is, in the words a person
// who has never heard of the product needs: the address, that it is sample
// data, that nothing is asked of them and how to end it.
func announceDemo(w io.Writer, url string) {
	fmt.Fprintf(w, "\nZoomies demo is running.\n\n  %s\n\n", url)
	fmt.Fprintln(w, "  It is a controller on this machine only, with a fleet already in it: pools,")
	fmt.Fprintln(w, "  hosts, runners and a morning's worth of jobs. Nobody has to sign in, GitHub")
	fmt.Fprintln(w, "  is not involved, and everything is deleted when you stop it with Ctrl-C.")
	fmt.Fprintln(w)
}

// openBrowser asks the desktop to open a page, and says whether it could.
func openBrowser(ctx context.Context, target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", target)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reaped in the background: the opener is a short-lived process of its own,
	// and a demo can run for hours.
	go func() { _ = cmd.Wait() }()
	return nil
}
