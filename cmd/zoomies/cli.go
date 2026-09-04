package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/version"
	"gopkg.in/yaml.v3"
)

// The commands in this file and the per-resource files beside it are pure API
// clients: they open no database and call into no controller, only the HTTP
// routes in api/openapi.yaml. That is what keeps the CLI honest -- anything it
// can do, the web UI can do, because both go through the same surface -- and it
// is worth preserving deliberately, since the daemon half of this binary sits
// in the same package and the compiler will not stop anyone reaching for it.

// defaultRequestTimeout bounds an ordinary call. Streams -- a followed log tail,
// the event stream -- set no deadline of their own and are bounded by the
// operator's patience and ctrl-C instead.
const defaultRequestTimeout = 30 * time.Second

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

// cliConfig is ~/.config/zoomies/cli.yaml: the credentials of last resort, for
// an operator who does not want ZOOMIES_TOKEN in their shell history.
type cliConfig struct {
	URL      string `yaml:"url"`
	Token    string `yaml:"token"`
	CAFile   string `yaml:"ca_file"`
	Insecure bool   `yaml:"insecure"`
}

// cliConfigPath is where that file lives.
//
// It is resolved explicitly rather than with os.UserConfigDir so that the path
// is the documented ~/.config/zoomies/cli.yaml on every platform, including
// macOS, where UserConfigDir would answer with an Application Support
// directory no documentation mentions.
func cliConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("ZOOMIES_CLI_CONFIG")); p != "" {
		return p
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "zoomies", "cli.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "zoomies", "cli.yaml")
	}
	return filepath.Join(home, ".config", "zoomies", "cli.yaml")
}

// loadCLIConfig reads the credentials file. A missing file is not an error:
// most people use the environment, and complaining about a file they never
// created would be noise.
func loadCLIConfig(path string) (cliConfig, error) {
	var c cliConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, fmt.Errorf("reading %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%s: %w (it should hold url, token and optionally ca_file and insecure)", path, err)
	}
	return c, nil
}

// clientFlags are the flags every command that talks to a controller accepts.
type clientFlags struct {
	url      *string
	token    *string
	caFile   *string
	insecure *bool
	timeout  *time.Duration
	output   *string
	// cmd names the command these flags belong to, so that a bad --output
	// points at `zoomies pools list --help` rather than something vaguer.
	cmd string
}

// registerClientFlags adds them to a command's flag set. withOutput is false
// for the commands that change something: --output on a delete would promise a
// document that does not exist.
func registerClientFlags(fs *flagSet, withOutput bool) *clientFlags {
	cf := &clientFlags{
		cmd:      fs.Name(),
		url:      fs.String("url", "", "the controller, e.g. https://zoomies.example.com (or ZOOMIES_URL)"),
		token:    fs.String("token", "", "an API token (or ZOOMIES_TOKEN)"),
		caFile:   fs.String("ca-file", "", "PEM file holding the controller's certificate"),
		insecure: fs.Bool("insecure", false, "do not verify the controller's certificate"),
		timeout:  fs.Duration("timeout", defaultRequestTimeout, "how long to wait for one request"),
	}
	if withOutput {
		cf.output = fs.String("output", outputTable, "table, json or yaml")
	}
	return cf
}

// printer builds the renderer for this command's --output.
func (cf *clientFlags) printer(e *env) (*printer, error) {
	format := outputTable
	if cf.output != nil {
		format = *cf.output
	}
	return newPrinter(e, cf.cmd, format)
}

// client resolves credentials and builds the API client.
//
// The order is flags, then environment, then the file: the most specific thing
// the operator typed wins, and the file is what makes a plain `zoomies status`
// work at all.
func (cf *clientFlags) client() (*apiClient, error) {
	file, err := loadCLIConfig(cliConfigPath())
	if err != nil {
		return nil, err
	}

	base := firstNonBlank(*cf.url, os.Getenv("ZOOMIES_URL"), file.URL)
	if base == "" {
		return nil, missingCredential("no controller URL", "url", "https://zoomies.example.com", "")
	}
	if !strings.Contains(base, "://") {
		// A bare host is what people type first. Assume https, because
		// silently downgrading somebody's credentials to http would be worse
		// than an error they can fix with six characters.
		base = "https://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("%q is not a controller URL; use something like https://zoomies.example.com", base)
	}

	token := firstNonBlank(*cf.token, os.Getenv("ZOOMIES_TOKEN"), file.Token)
	caFile := firstNonBlank(*cf.caFile, os.Getenv("ZOOMIES_CA_FILE"), file.CAFile)
	insecure := *cf.insecure || file.Insecure

	httpc, err := httpClient(caFile, insecure, *cf.timeout)
	if err != nil {
		return nil, err
	}
	return &apiClient{
		base:    strings.TrimRight(parsed.String(), "/"),
		token:   token,
		http:    httpc,
		timeout: *cf.timeout,
	}, nil
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// missingCredential writes the one error message that has to be complete: it
// names all three places the value is looked for, in the order they are
// consulted, and -- for a token -- how to get one.
func missingCredential(what, key, example, why string) error {
	var b strings.Builder
	b.WriteString(what)
	if why != "" {
		b.WriteString(": ")
		b.WriteString(why)
	}
	b.WriteString(".\nSet one of these, in the order zoomies looks at them:\n")
	fmt.Fprintf(&b, "  --%s %s\n", key, example)
	fmt.Fprintf(&b, "  ZOOMIES_%s=%s\n", strings.ToUpper(key), example)
	fmt.Fprintf(&b, "  %s: %s   in %s\n", key, example, cliConfigPath())
	if key == "token" {
		b.WriteString("Mint a token in the UI under Settings -> API tokens, where it is shown once,\n")
		b.WriteString("or, with an admin token already to hand:\n")
		b.WriteString("  zoomies tokens create --name my-laptop --role operator")
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}

// ---------------------------------------------------------------------------
// The client
// ---------------------------------------------------------------------------

// apiClient speaks the REST API in api/openapi.yaml and nothing else.
type apiClient struct {
	base    string
	token   string
	http    *http.Client
	timeout time.Duration
}

// endpoint builds an absolute URL for an API path such as "/pools".
func (c *apiClient) endpoint(path string, q url.Values) string {
	u := c.base + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// apiError is a response the controller refused, kept whole so that the field
// errors from a 422 survive to the operator's terminal.
type apiError struct {
	status  int
	method  string
	path    string
	code    string
	message string
	field   string
	detail  string
	fields  []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	}
}

func (e *apiError) Error() string {
	var b strings.Builder
	msg := e.message
	if msg == "" {
		msg = http.StatusText(e.status)
	}
	switch e.status {
	case http.StatusUnauthorized:
		return msg + "\n" + missingCredential("the controller rejected this request", "token", "zoo_...", "no usable API token was sent").Error()
	case http.StatusForbidden:
		return msg + " (this token's role is not enough for " + e.method + " " + e.path + ")"
	}
	b.WriteString(msg)
	if e.field != "" {
		fmt.Fprintf(&b, " (field %s)", e.field)
	}
	if e.detail != "" {
		fmt.Fprintf(&b, "\n  %s", e.detail)
	}
	for _, f := range e.fields {
		fmt.Fprintf(&b, "\n  %s: %s", f.Field, f.Message)
	}
	return b.String()
}

// notFound reports whether the controller said there is no such thing, which a
// few commands turn into a friendlier sentence naming what was looked for.
func notFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == http.StatusNotFound
}

// do performs one request and returns the body. out, when not nil, is decoded
// from it.
func (c *apiClient) do(ctx context.Context, method, path string, q url.Values, body, out any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	target := c.endpoint(path, q)
	req, err := http.NewRequestWithContext(reqCtx, method, target, reader)
	if err != nil {
		return nil, err
	}
	c.decorate(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if stopped(ctx, err) {
			return nil, context.Canceled
		}
		return nil, c.transportError(method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the answer to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		return raw, parseAPIError(method, path, resp.StatusCode, raw)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, fmt.Errorf("the controller's answer to %s %s was not the JSON this version expects: %w", method, path, err)
		}
	}
	return raw, nil
}

func (c *apiClient) get(ctx context.Context, path string, q url.Values, out any) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, q, nil, out)
}

func (c *apiClient) post(ctx context.Context, path string, q url.Values, body, out any) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, q, body, out)
}

func (c *apiClient) patch(ctx context.Context, path string, q url.Values, body, out any) ([]byte, error) {
	return c.do(ctx, http.MethodPatch, path, q, body, out)
}

func (c *apiClient) del(ctx context.Context, path string, q url.Values, out any) ([]byte, error) {
	return c.do(ctx, http.MethodDelete, path, q, nil, out)
}

// stream opens a response the caller reads until it ends: an SSE stream or a
// log download. It sets no timeout of its own, which is the whole point.
func (c *apiClient) stream(ctx context.Context, path string, q url.Values, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path, q), nil)
	if err != nil {
		return nil, err
	}
	c.decorate(req)
	req.Header.Set("Accept", accept)

	resp, err := c.http.Do(req)
	if err != nil {
		if stopped(ctx, err) {
			return nil, context.Canceled
		}
		return nil, c.transportError(http.MethodGet, path, err)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, parseAPIError(http.MethodGet, path, resp.StatusCode, raw)
	}
	return resp, nil
}

func (c *apiClient) decorate(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "zoomies-cli/"+version.Version)
	if c.token != "" {
		// A bearer token is exempt from the same-origin check, which is why
		// the CLI never has to think about Origin headers.
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// stopped reports whether an error is only the operator's ctrl-C.
//
// It asks the context rather than inspecting the error, because a cancelled
// request surfaces as whatever the transport was doing at the time -- and
// because signal.NotifyContext attaches a cause of its own, so the error a
// stream ends with is not context.Canceled and must not be printed as a
// failure.
func stopped(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

// transportError turns a dial or TLS failure into something actionable. "connection
// refused" on its own does not say which address was tried or how it was chosen.
func (c *apiClient) transportError(method, path string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s %s timed out after %s; raise --timeout, or check that %s is reachable from here", method, path, c.timeout, c.base)
	}
	return fmt.Errorf("cannot reach the controller at %s: %w\n"+
		"  check that it is running, that --url (or ZOOMIES_URL) is right, and that nothing between you and it is blocking the port", c.base, err)
}

// parseAPIError decodes the error envelope every route returns.
func parseAPIError(method, path string, status int, raw []byte) error {
	e := &apiError{status: status, method: method, path: path}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Field   string `json:"field"`
			Detail  string `json:"detail"`
		} `json:"error"`
		Errors []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		e.code = envelope.Error.Code
		e.message = envelope.Error.Message
		e.field = envelope.Error.Field
		e.detail = envelope.Error.Detail
		e.fields = envelope.Errors
	}
	if e.message == "" {
		// Not our envelope at all: a proxy's error page, most likely. Show a
		// little of it, because "502" without the body is a mystery.
		body := strings.TrimSpace(string(raw))
		if len(body) > 200 {
			body = body[:200] + "…"
		}
		e.message = fmt.Sprintf("%s %s answered %d %s", method, path, status, http.StatusText(status))
		if body != "" {
			e.detail = body
		}
	}
	return e
}

// ---------------------------------------------------------------------------
// Server-Sent Events
// ---------------------------------------------------------------------------

// sseFrame is one event off a stream.
type sseFrame struct {
	event string
	data  []byte
}

// readSSE parses an event stream and calls fn for each complete frame. It
// returns when the stream ends, when fn returns an error, or when the reader is
// closed under it -- which is what cancelling the context does.
//
// bufio.Reader rather than Scanner because a single line of a build's output
// can be longer than Scanner's maximum token, and a log tail that stops on the
// one interesting line would be worse than useless.
func readSSE(r io.Reader, fn func(sseFrame) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var frame sseFrame
	var data bytes.Buffer

	for {
		line, err := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				// Blank line: the frame is complete.
				if frame.event != "" || data.Len() > 0 {
					frame.data = bytes.Clone(data.Bytes())
					if ferr := fn(frame); ferr != nil {
						return ferr
					}
				}
				frame = sseFrame{}
				data.Reset()
			case strings.HasPrefix(line, ":"):
				// A comment, which is how the server keeps the connection warm.
			case strings.HasPrefix(line, "event:"):
				frame.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			// A stream that ends without a final blank line still has a frame
			// worth delivering: the last chunk of a job's output should not be
			// lost because the connection closed a byte early.
			if frame.event != "" || data.Len() > 0 {
				frame.data = bytes.Clone(data.Bytes())
				return fn(frame)
			}
			return nil
		}
	}
}
