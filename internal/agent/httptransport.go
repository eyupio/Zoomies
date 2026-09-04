package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/version"
)

// ErrUnauthorized reports that the controller rejected this agent's token. It
// is terminal: retrying with the same token will fail forever, so the agent
// stops rather than hammering the controller with credentials it has revoked.
var ErrUnauthorized = errors.New("agent: the controller rejected this agent's token; re-join the host with a fresh join token (zoomies agent join <controller-url> --token <join-token>)")

// ErrHostGone reports that the controller no longer has a record of this host,
// which happens when the host was deleted in the UI while the agent was down.
// The agent cannot invent itself back into the fleet; an operator must re-join.
var ErrHostGone = errors.New("agent: the controller no longer knows this host; re-join it with a fresh join token (zoomies agent join <controller-url> --token <join-token>)")

// ErrRetryable marks a failure that is worth trying again: a controller
// restart, a proxy hiccup, a flapping link. The agent backs off on these rather
// than exiting, because a host that gives up on a transient network fault takes
// its runners out of the fleet for no reason.
var ErrRetryable = errors.New("agent: transient failure talking to the controller")

// HeaderHostID carries the agent's host ID alongside its bearer token, so that
// a controller log line can name the host without first looking the token up.
const HeaderHostID = "X-Zoomies-Host"

// Sizes and deadlines for the request path.
const (
	// defaultCallTimeout bounds every request except a task long poll, which
	// sets its own deadline from the wait it asked for.
	defaultCallTimeout = 30 * time.Second
	// pollMargin is added to the requested wait when a long poll's deadline is
	// set. The request must outlive the wait the controller was asked for:
	// giving a long poll an ordinary timeout is the classic bug that turns it
	// into a tight retry loop hammering the controller once per timeout.
	pollMargin = 15 * time.Second
	// logStreamCloseWait bounds how long Close waits for the controller to
	// acknowledge a finished log stream before abandoning the request.
	logStreamCloseWait = 30 * time.Second

	maxResponseBytes = 8 << 20
	maxErrorBytes    = 4 << 10
)

// userAgent identifies this agent to the controller, which logs it and uses it
// to spot a fleet that was only half upgraded.
var userAgent = "zoomies-agent/" + version.Version

// HTTPOptions configures an HTTPTransport.
type HTTPOptions struct {
	// ControllerURL is where the controller answers, e.g.
	// https://zoomies.internal:8080.
	ControllerURL string
	// CAFile pins the certificate the controller presents. For the usual
	// self-signed controller this is certificate pinning, not a public CA: it
	// is what makes a private deployment verifiable without buying a name.
	CAFile string
	// ClientCertFile and ClientKeyFile enable mTLS. Both or neither.
	ClientCertFile string
	ClientKeyFile  string
	// InsecureSkipVerify disables verification entirely. Every construction
	// with it set logs what it costs.
	InsecureSkipVerify bool
	// HTTPClient replaces the transport's own client. Tests use it; production
	// leaves it nil so that the TLS settings above take effect.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// HTTPTransport is the Transport a standalone agent uses: every call is an
// outbound request to the controller, so the host needs no inbound firewall
// rule and can sit behind NAT.
//
// It is safe for concurrent use; the agent heartbeats, polls and streams logs
// at the same time.
type HTTPTransport struct {
	base string
	http *http.Client
	log  *slog.Logger

	mu         sync.RWMutex
	hostID     string
	agentToken string
}

// NewHTTPTransport builds a transport pointed at one controller.
func NewHTTPTransport(opts HTTPOptions) (*HTTPTransport, error) {
	raw := strings.TrimSpace(opts.ControllerURL)
	if raw == "" {
		return nil, errors.New("agent: no controller URL; set agent.controller_url in zoomies.yaml or pass it to `zoomies agent join`")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("agent: %q is not a URL; use something like https://zoomies.example.com:8080: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("agent: controller URL %q must start with http:// or https://", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("agent: controller URL %q has no host; use something like https://zoomies.example.com:8080", raw)
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "agent.transport")

	tlsCfg, err := buildTLSConfig(opts, u, log)
	if err != nil {
		return nil, err
	}

	client := opts.HTTPClient
	if client == nil {
		// No Client.Timeout on purpose. A global timeout would abort a task
		// long poll and a followed log stream, both of which are idle for
		// minutes by design; every call sets its own deadline on the context
		// instead.
		client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:       tlsCfg,
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:          8,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
				ForceAttemptHTTP2:     true,
			},
		}
	}

	return &HTTPTransport{
		base: strings.TrimSuffix(u.String(), "/"),
		http: client,
		log:  log,
	}, nil
}

// buildTLSConfig turns the operator's certificate settings into a tls.Config,
// failing early with a message that names the setting at fault.
func buildTLSConfig(opts HTTPOptions, u *url.URL, log *slog.Logger) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if opts.InsecureSkipVerify {
		// Warned on every construction, not once at startup, because the
		// failure mode is silent: anything that can intercept this connection
		// sees the JIT configurations flowing to this host and can hand the
		// agent tasks of its own.
		log.Warn("controller certificate verification is disabled; anything able to intercept this connection can read runner credentials and issue tasks to this host",
			"setting", "agent.insecure_skip_verify",
			"controller", u.Redacted(),
			"fix", "pin the controller's certificate with agent.ca_file instead")
		cfg.InsecureSkipVerify = true
	}

	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("agent: reading agent.ca_file %s: %w", opts.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("agent: %s contains no PEM certificate; agent.ca_file must be the controller's certificate (or its CA) in PEM form -- copy /var/lib/zoomies/tls/cert.pem from the controller", opts.CAFile)
		}
		cfg.RootCAs = pool
	}

	switch {
	case opts.ClientCertFile != "" && opts.ClientKeyFile != "":
		cert, err := tls.LoadX509KeyPair(opts.ClientCertFile, opts.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("agent: loading the client certificate from agent.client_cert_file %s and agent.client_key_file %s: %w", opts.ClientCertFile, opts.ClientKeyFile, err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	case opts.ClientCertFile != "":
		return nil, errors.New("agent: agent.client_cert_file is set without agent.client_key_file; mTLS needs both")
	case opts.ClientKeyFile != "":
		return nil, errors.New("agent: agent.client_key_file is set without agent.client_cert_file; mTLS needs both")
	}

	return cfg, nil
}

// HTTPError is a controller response the agent could not use. It carries the
// status so that callers can react to a specific one (a 404 on heartbeat means
// something quite different from a 404 on a result).
type HTTPError struct {
	Status int
	Method string
	Path   string
	// Body is a truncated copy of the response, which is usually the
	// controller's own error message and the most useful thing in the log.
	Body string
	// Retry records whether trying the same request again could work.
	Retry bool
}

func (e *HTTPError) Error() string {
	detail := ""
	if e.Body != "" {
		detail = ": " + e.Body
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return fmt.Sprintf("agent: %s %s was rejected: the controller does not accept this agent's token; re-join the host with a fresh join token (zoomies agent join <controller-url> --token <join-token>)%s", e.Method, e.Path, detail)
	case http.StatusForbidden:
		return fmt.Sprintf("agent: %s %s was refused: this agent's token is not allowed to do that; check the host has not been cordoned or its token scoped down%s", e.Method, e.Path, detail)
	case http.StatusNotFound:
		return fmt.Sprintf("agent: %s %s: the controller has no such endpoint or object; it may be older than this agent (agent protocol %d) -- upgrade the controller%s", e.Method, e.Path, ProtocolVersion, detail)
	default:
		return fmt.Sprintf("agent: %s %s returned HTTP %d%s", e.Method, e.Path, e.Status, detail)
	}
}

// Is lets callers match the sentinels without unwrapping a status code, so the
// agent's loops read as "unauthorized" and "retryable" rather than as numbers.
func (e *HTTPError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrRetryable:
		return e.Retry
	}
	return false
}

// Retryable reports whether err is worth trying again. Rejected credentials and
// a deleted host are not; everything else that went wrong on the wire is.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrHostGone) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrRetryable) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// SetCredentials installs the identity Join returned, or the one restored from
// the agent's state file.
func (t *HTTPTransport) SetCredentials(hostID, agentToken string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hostID, t.agentToken = hostID, agentToken
}

// Describe returns the controller URL, for the startup banner and log lines.
func (t *HTTPTransport) Describe() string { return t.base }

func (t *HTTPTransport) credentials() (string, string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hostID, t.agentToken
}

// request is one call to the controller, described declaratively so that the
// header, deadline and error handling live in exactly one place.
type request struct {
	method string
	path   string
	query  url.Values
	body   any
	out    any
	// anonymous omits the credentials. Only Join is anonymous, because it is
	// the call that mints them.
	anonymous bool
	timeout   time.Duration
}

func (t *HTTPTransport) call(ctx context.Context, r request) (int, error) {
	var body io.Reader
	if r.body != nil {
		raw, err := json.Marshal(r.body)
		if err != nil {
			return 0, fmt.Errorf("agent: encoding the %s request body: %w", r.path, err)
		}
		body = bytes.NewReader(raw)
	}

	target := t.base + r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}

	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, r.method, target, body)
	if err != nil {
		return 0, fmt.Errorf("agent: building a %s request to %s: %w", r.method, target, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !r.anonymous {
		hostID, token := t.credentials()
		if token == "" {
			return 0, fmt.Errorf("%w: no agent token loaded yet for %s %s", ErrNotJoined, r.method, r.path)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(HeaderHostID, hostID)
	}

	resp, err := t.http.Do(req)
	if err != nil {
		// A dial failure or a dropped connection is a controller restart or a
		// flapping network, both of which come back on their own.
		return 0, fmt.Errorf("agent: %s %s: %w: %w", r.method, r.path, ErrRetryable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return resp.StatusCode, statusError(r.method, r.path, resp)
	}
	if r.out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(r.out); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty 200 is the controller saying "nothing to tell you".
			return resp.StatusCode, nil
		}
		return resp.StatusCode, fmt.Errorf("agent: %s %s returned a body this agent could not read; the controller may be a different version (agent protocol %d): %w", r.method, r.path, ProtocolVersion, err)
	}
	return resp.StatusCode, nil
}

func statusError(method, path string, resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	return &HTTPError{
		Status: resp.StatusCode,
		Method: method,
		Path:   path,
		Body:   strings.TrimSpace(string(snippet)),
		Retry: resp.StatusCode >= 500 ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusRequestTimeout,
	}
}

// Join redeems a join token. It is the one call made without credentials,
// because it is the call that issues them.
func (t *HTTPTransport) Join(ctx context.Context, req JoinRequest) (*JoinResponse, error) {
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = ProtocolVersion
	}
	var out JoinResponse
	if _, err := t.call(ctx, request{method: http.MethodPost, path: PathJoin, body: req, out: &out, anonymous: true}); err != nil {
		return nil, err
	}
	if out.HostID == "" || out.AgentToken == "" {
		return nil, fmt.Errorf("agent: %s accepted the join but returned no host ID or agent token; the controller is probably older than this agent (agent protocol %d)", t.base, ProtocolVersion)
	}
	return &out, nil
}

// Heartbeat reports liveness and the agent's own view of its runners.
func (t *HTTPTransport) Heartbeat(ctx context.Context, req HeartbeatRequest) (*HeartbeatResponse, error) {
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = ProtocolVersion
	}
	if req.Version == "" {
		req.Version = version.Version
	}
	var out HeartbeatResponse
	status, err := t.call(ctx, request{method: http.MethodPost, path: PathHeartbeat, body: req, out: &out})
	if err != nil {
		// A 404 here is specifically "I have no host with that ID": the row was
		// deleted, and no amount of retrying will bring it back.
		if status == http.StatusNotFound {
			hostID, _ := t.credentials()
			return nil, fmt.Errorf("%w (host %s): %w", ErrHostGone, hostID, err)
		}
		return nil, err
	}
	return &out, nil
}

// PollTasks long-polls for work. An empty batch after the full wait is the
// normal idle case.
func (t *HTTPTransport) PollTasks(ctx context.Context, wait time.Duration) (*TaskBatch, error) {
	if wait <= 0 {
		wait = DefaultPollWait
	}
	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}

	var out TaskBatch
	// The deadline is the wait plus a margin, never the ordinary call timeout:
	// a long poll that is cut off at its own wait can never return work.
	_, err := t.call(ctx, request{
		method:  http.MethodGet,
		path:    PathTasks,
		query:   url.Values{"wait": {strconv.Itoa(seconds)}},
		out:     &out,
		timeout: wait + pollMargin,
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ReportResult reports the outcome of one task.
func (t *HTTPTransport) ReportResult(ctx context.Context, res TaskResult) error {
	_, err := t.call(ctx, request{method: http.MethodPost, path: PathResults, body: res})
	return err
}

// ReportRunners pushes runner observations outside the heartbeat cycle so a
// state change shows up in the UI immediately.
func (t *HTTPTransport) ReportRunners(ctx context.Context, reports []RunnerReport) error {
	if len(reports) == 0 {
		return nil
	}
	_, err := t.call(ctx, request{method: http.MethodPost, path: PathReport, body: reports})
	return err
}

// OpenLogStream starts a chunked POST the agent writes a runner's output into.
// The controller fans the body out to whoever is watching in the browser, which
// is how log streaming works at all when the controller can never dial in.
func (t *HTTPTransport) OpenLogStream(ctx context.Context, streamID string) (io.WriteCloser, error) {
	if strings.TrimSpace(streamID) == "" {
		return nil, errors.New("agent: cannot open a log stream without a stream ID; the controller must set one on the stream_logs task")
	}
	hostID, token := t.credentials()
	if token == "" {
		return nil, fmt.Errorf("%w: no agent token loaded yet, cannot open a log stream", ErrNotJoined)
	}

	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	target := t.base + PathLogs + "/" + url.PathEscape(streamID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, pr)
	if err != nil {
		cancel()
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("agent: building the log stream request to %s: %w", target, err)
	}
	// Length is unknown, so net/http chunks the body and the controller sees
	// output as it is produced instead of when the runner exits.
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(HeaderHostID, hostID)

	s := &logStreamWriter{pw: pw, cancel: cancel, done: make(chan struct{}), streamID: streamID}
	go func() {
		defer close(s.done)
		resp, err := t.http.Do(req)
		if err != nil {
			s.fail(fmt.Errorf("agent: log stream %s to %s: %w: %w", streamID, target, ErrRetryable, err))
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
		if resp.StatusCode >= 300 {
			s.fail(statusError(http.MethodPost, PathLogs+"/"+streamID, resp))
		}
	}()
	return s, nil
}

// logStreamWriter is the writer half of one relayed log stream. Errors from the
// request goroutine are surfaced from Write and Close rather than swallowed: a
// relay that silently stopped delivering looks exactly like a quiet runner.
type logStreamWriter struct {
	streamID string
	pw       *io.PipeWriter
	cancel   context.CancelFunc
	done     chan struct{}

	mu        sync.Mutex
	err       error
	closeOnce sync.Once
}

func (s *logStreamWriter) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	// Unblock a writer sitting in Write: the request is over, so nothing will
	// ever read from the pipe again.
	s.pw.CloseWithError(err)
}

func (s *logStreamWriter) result() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *logStreamWriter) Write(p []byte) (int, error) {
	if err := s.result(); err != nil {
		return 0, err
	}
	n, err := s.pw.Write(p)
	if err != nil {
		// Prefer the request's own error: "the controller returned 503" is
		// actionable, "io: read/write on closed pipe" is not.
		if reqErr := s.result(); reqErr != nil {
			return n, reqErr
		}
		return n, fmt.Errorf("agent: writing to log stream %s: %w", s.streamID, err)
	}
	return n, nil
}

// Close ends the request, waits for the controller's response so that a
// rejected stream is reported to the caller, and gives up after
// logStreamCloseWait so a wedged controller cannot hold the agent's shutdown.
func (s *logStreamWriter) Close() error {
	s.closeOnce.Do(func() {
		s.pw.Close()
		abandon := time.AfterFunc(logStreamCloseWait, s.cancel)
		<-s.done
		abandon.Stop()
		s.cancel()
	})
	return s.result()
}
