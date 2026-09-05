package backend

// This file is a small, hand-written client for the Docker Engine API.
//
// Why not github.com/docker/docker? Because importing the official client pulls
// in most of Docker itself -- containerd, swarmkit, opencontainers, logrus, and
// a few hundred transitive packages -- to make a dozen HTTP calls. Zoomies is a
// single binary an operator downloads and runs, so its dependency tree is part
// of its user interface: every module in it is something they have to trust,
// audit and rebuild when a CVE lands. The Engine API is a documented, stable,
// versioned REST API over a unix socket; speaking it with net/http costs one
// file and buys us Podman for free, because podman.sock serves the same API.
//
// The parts that are genuinely fiddly -- the multiplexed log framing and the
// stats deltas -- are implemented here once and unit tested, which is more than
// the dependency would have bought us.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// APIVersion is the Engine API version every request is prefixed with.
//
// It is deliberately old: 1.41 is served by Docker 20.10 (still shipped by
// long-term-support distributions) and is the highest version Podman 4's
// compatibility layer advertises. Everything Zoomies needs -- containers, logs,
// stats, networks -- has been in the API since well before it.
const APIVersion = "v1.41"

// Timeouts for the request/response path. Streaming calls (logs, image pulls)
// deliberately escape the response timeout: a followed log stream is idle for
// minutes at a time by design, and a cold image pull can take a quarter of an
// hour on a slow link.
const (
	dialTimeout           = 5 * time.Second
	responseHeaderTimeout = 30 * time.Second
	defaultCallTimeout    = 60 * time.Second
)

// APIClient talks to a Docker-compatible Engine API over a unix socket or TCP.
//
// It is safe for concurrent use; the embedded http.Client pools connections.
type APIClient struct {
	// host is the endpoint exactly as the operator wrote it, so that error
	// messages name the thing in their configuration file.
	host    string
	http    *http.Client
	version string

	// base is the URL prefix requests are built on. For a unix socket the
	// authority is a placeholder, since the transport ignores it.
	base string
	// socket is the filesystem path of the unix socket, empty for TCP. It is
	// what Probe reports as the host socket path and what the diagnostics in
	// detect.go inspect.
	socket string
}

// NewAPIClient builds a client for a Docker host.
//
// Accepted forms are "unix:///var/run/docker.sock", "tcp://host:2375",
// "http(s)://host:port" and a bare filesystem path. Windows named pipes are not
// supported: Zoomies agents run runners on Linux and macOS hosts.
func NewAPIClient(host string) (*APIClient, error) {
	h := strings.TrimSpace(host)
	if h == "" {
		return nil, errors.New("backend: no Docker host given; set agent.docker_host in zoomies.yaml or leave it empty to autodetect")
	}

	c := &APIClient{host: h, version: APIVersion}
	switch {
	case strings.HasPrefix(h, "npipe://"):
		return nil, fmt.Errorf("backend: %q is a Windows named pipe, which Zoomies does not support; run the agent on a Linux or macOS host, or point agent.docker_host at a tcp:// endpoint", h)
	case strings.HasPrefix(h, "ssh://"):
		return nil, fmt.Errorf("backend: %q is an SSH endpoint, which Zoomies does not support; run a Zoomies agent on that host instead and let it connect outbound", h)
	case strings.HasPrefix(h, "unix://"):
		c.socket = strings.TrimPrefix(h, "unix://")
		c.base = "http://docker"
	case strings.HasPrefix(h, "tcp://"):
		c.base = "http://" + strings.TrimPrefix(h, "tcp://")
	case strings.HasPrefix(h, "http://"), strings.HasPrefix(h, "https://"):
		c.base = h
	case strings.HasPrefix(h, "/"):
		c.socket = h
		c.base = "http://docker"
	default:
		return nil, fmt.Errorf("backend: %q is not a Docker host; use unix:///var/run/docker.sock, tcp://host:2375 or an absolute socket path", h)
	}
	c.base = strings.TrimSuffix(c.base, "/")

	tr := &http.Transport{
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		DisableCompression:    true,
	}
	if c.socket != "" {
		sock := c.socket
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			d.Timeout = dialTimeout
			return d.DialContext(ctx, "unix", sock)
		}
	} else {
		tr.DialContext = (&net.Dialer{Timeout: dialTimeout}).DialContext
	}
	// Client.Timeout would abort a followed log stream, so the deadline lives
	// on the context of each non-streaming call instead.
	c.http = &http.Client{Transport: tr}
	return c, nil
}

// Endpoint returns the host string this client was built from.
func (c *APIClient) Endpoint() string { return c.host }

// SocketPath returns the unix socket in use, or "" for a TCP endpoint.
func (c *APIClient) SocketPath() string { return c.socket }

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// APIError is a non-2xx response from the daemon, carrying the daemon's own
// explanation so an operator sees "no such image" rather than "status 404".
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("docker api: %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// Unwrap maps a 404 onto ErrNotFound so callers can treat "already gone" as
// success without inspecting status codes.
func (e *APIError) Unwrap() error {
	if e.Status == http.StatusNotFound {
		return ErrNotFound
	}
	return nil
}

// StatusCode returns the HTTP status an error carries, or 0 if it did not come
// from the daemon.
func StatusCode(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// unavailable wraps a transport failure with advice, because "connect:
// permission denied" on its own has sent many operators to the wrong place.
func (c *APIClient) unavailable(err error) error {
	where := c.host
	switch {
	case errors.Is(err, syscall.ENOENT):
		return fmt.Errorf("%w: no socket at %s; the daemon is not running or is listening elsewhere -- start it (systemctl --user start docker, or systemctl start docker) or set agent.docker_host: %w", ErrUnavailable, where, err)
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		// deniedDetail names this agent's own account and the group that owns
		// the socket, which is what makes the usermod line copyable.
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, deniedDetail(realIdentity(), strings.TrimPrefix(where, "unix://")), err)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("%w: nothing is listening on %s; the socket exists but the daemon is down -- start it with systemctl --user start docker (rootless) or systemctl start docker: %w", ErrUnavailable, where, err)
	default:
		return fmt.Errorf("%w: cannot reach the Docker daemon at %s: %w", ErrUnavailable, where, err)
	}
}

// ---------------------------------------------------------------------------
// Request plumbing
// ---------------------------------------------------------------------------

func (c *APIClient) urlFor(path string, q url.Values) string {
	u := c.base + "/" + c.version + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// doRaw performs one request and hands back the live response. The caller owns
// the body. Streaming callers pass stream=true to keep the context deadline the
// caller chose instead of the default call timeout.
func (c *APIClient) doRaw(ctx context.Context, method, path string, q url.Values, body any, headers map[string]string, stream bool) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("docker api: encoding the %s %s request: %w", method, path, err)
		}
		rdr = strings.NewReader(string(b))
	}

	if !stream {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
			defer cancel()
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.urlFor(path, q), rdr)
	if err != nil {
		return nil, fmt.Errorf("docker api: building the %s %s request: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.unavailable(err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, &APIError{Status: resp.StatusCode, Method: method, Path: path, Message: decodeMessage(resp.Body)}
	}
	return resp, nil
}

// do performs a request, optionally decodes a JSON response into out, and
// always closes the body.
func (c *APIClient) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := c.doRaw(ctx, method, path, q, body, nil, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker api: decoding the %s %s response: %w", method, path, err)
	}
	return nil
}

// decodeMessage pulls the daemon's {"message": "..."} out of an error body,
// falling back to the raw text so nothing is lost when a proxy answers instead.
func decodeMessage(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil || len(b) == 0 {
		return ""
	}
	var env struct {
		Message string `json:"message"`
		Cause   string `json:"cause"`
	}
	if err := json.Unmarshal(b, &env); err == nil && env.Message != "" {
		return env.Message
	}
	return strings.TrimSpace(string(b))
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// VersionInfo is the /version payload.
type VersionInfo struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	GitCommit     string `json:"GitCommit"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
	KernelVersion string `json:"KernelVersion"`
	// Components names the engine parts; Podman identifies itself here as
	// "Podman Engine" while still reporting a Docker API version.
	Components []struct {
		Name    string `json:"Name"`
		Version string `json:"Version"`
	} `json:"Components,omitempty"`
}

// SystemInfo is the subset of /info Zoomies uses. SecurityOptions is how
// rootless mode is detected on both Docker and Podman.
type SystemInfo struct {
	ID              string   `json:"ID"`
	Name            string   `json:"Name"`
	ServerVersion   string   `json:"ServerVersion"`
	OperatingSystem string   `json:"OperatingSystem"`
	OSType          string   `json:"OSType"`
	Architecture    string   `json:"Architecture"`
	NCPU            int      `json:"NCPU"`
	MemTotal        int64    `json:"MemTotal"`
	Driver          string   `json:"Driver"`
	CgroupDriver    string   `json:"CgroupDriver"`
	CgroupVersion   string   `json:"CgroupVersion"`
	DockerRootDir   string   `json:"DockerRootDir"`
	SecurityOptions []string `json:"SecurityOptions"`
	// Rootless is not part of Docker's /info, but Podman's compatibility
	// endpoint sets it, so it is worth reading when present.
	Rootless bool `json:"Rootless,omitempty"`
}

// ContainerCreateRequest is the POST /containers/create body.
type ContainerCreateRequest struct {
	Hostname     string            `json:"Hostname,omitempty"`
	User         string            `json:"User,omitempty"`
	Env          []string          `json:"Env,omitempty"`
	Cmd          []string          `json:"Cmd,omitempty"`
	Entrypoint   []string          `json:"Entrypoint,omitempty"`
	Image        string            `json:"Image"`
	WorkingDir   string            `json:"WorkingDir,omitempty"`
	Labels       map[string]string `json:"Labels,omitempty"`
	Tty          bool              `json:"Tty"`
	OpenStdin    bool              `json:"OpenStdin"`
	AttachStdout bool              `json:"AttachStdout"`
	AttachStderr bool              `json:"AttachStderr"`
	StopSignal   string            `json:"StopSignal,omitempty"`

	HostConfig       *HostConfig       `json:"HostConfig,omitempty"`
	NetworkingConfig *NetworkingConfig `json:"NetworkingConfig,omitempty"`
}

// HostConfig carries the isolation and resource settings.
type HostConfig struct {
	Binds          []string          `json:"Binds,omitempty"`
	NetworkMode    string            `json:"NetworkMode,omitempty"`
	AutoRemove     bool              `json:"AutoRemove"`
	Privileged     bool              `json:"Privileged,omitempty"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs,omitempty"`
	CapAdd         []string          `json:"CapAdd,omitempty"`
	CapDrop        []string          `json:"CapDrop,omitempty"`
	SecurityOpt    []string          `json:"SecurityOpt,omitempty"`
	GroupAdd       []string          `json:"GroupAdd,omitempty"`
	ExtraHosts     []string          `json:"ExtraHosts,omitempty"`
	NanoCPUs       int64             `json:"NanoCpus,omitempty"`
	Memory         int64             `json:"Memory,omitempty"`
	MemorySwap     int64             `json:"MemorySwap,omitempty"`
	PidsLimit      *int64            `json:"PidsLimit,omitempty"`
	ShmSize        int64             `json:"ShmSize,omitempty"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	RestartPolicy  RestartPolicy     `json:"RestartPolicy"`
}

// RestartPolicy is always "no" for runners: a runner that died has either
// finished its one job or failed, and both are the controller's business.
type RestartPolicy struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount"`
}

// NetworkingConfig attaches a container to a network at creation time.
type NetworkingConfig struct {
	EndpointsConfig map[string]*EndpointSettings `json:"EndpointsConfig,omitempty"`
}

// EndpointSettings configures one network attachment.
type EndpointSettings struct {
	Aliases []string `json:"Aliases,omitempty"`
}

// ContainerConfig is the config half of an inspect response.
type ContainerConfig struct {
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
	Env    []string          `json:"Env"`
	Tty    bool              `json:"Tty"`
	User   string            `json:"User"`
}

// ContainerState is the state half of an inspect response. The timestamps are
// strings because Docker sends "0001-01-01T00:00:00Z" for "never", and Podman
// has been known to send an empty string.
type ContainerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	Dead       bool   `json:"Dead"`
	Pid        int    `json:"Pid"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
}

// ContainerInspect is GET /containers/{id}/json.
type ContainerInspect struct {
	ID         string           `json:"Id"`
	Name       string           `json:"Name"`
	Created    string           `json:"Created"`
	State      *ContainerState  `json:"State"`
	Config     *ContainerConfig `json:"Config"`
	HostConfig *HostConfig      `json:"HostConfig"`
}

// ContainerSummary is one entry of GET /containers/json.
type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

// LogQuery selects what a log request returns.
type LogQuery struct {
	Stdout     bool
	Stderr     bool
	Follow     bool
	Timestamps bool
	// Tail limits the initial backlog; 0 means everything.
	Tail  int
	Since time.Time
}

// StatsSample is one resource reading, already reduced from the daemon's
// cumulative counters.
type StatsSample struct {
	CPUPercent  float64
	MemoryBytes int64
	MemoryLimit int64
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// Ping checks that the daemon answers.
func (c *APIClient) Ping(ctx context.Context) error {
	resp, err := c.doRaw(ctx, http.MethodGet, "/_ping", nil, nil, nil, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return nil
}

// Version reports the daemon's version.
func (c *APIClient) Version(ctx context.Context) (VersionInfo, error) {
	var v VersionInfo
	err := c.do(ctx, http.MethodGet, "/version", nil, nil, &v)
	return v, err
}

// Info reports the daemon's configuration, including the security options that
// reveal whether it is running rootless.
func (c *APIClient) Info(ctx context.Context) (SystemInfo, error) {
	var i SystemInfo
	err := c.do(ctx, http.MethodGet, "/info", nil, nil, &i)
	return i, err
}

// ImagePull fetches an image and waits for the pull to finish.
//
// The daemon answers immediately and then streams progress, so the pull is only
// complete once the stream ends. auth, when set, is the base64 X-Registry-Auth
// header value for a private registry.
func (c *APIClient) ImagePull(ctx context.Context, ref, auth string) error {
	name, tag := splitImageRef(ref)
	q := url.Values{"fromImage": {name}}
	if tag != "" {
		q.Set("tag", tag)
	}
	headers := map[string]string{"X-Registry-Auth": auth}

	resp, err := c.doRaw(ctx, http.MethodPost, "/images/create", q, nil, headers, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Progress arrives as a stream of JSON objects; an "errorDetail" anywhere in
	// it means the pull failed even though the request itself returned 200.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("docker api: pulling %s: reading the progress stream: %w", ref, err)
		}
		if m := firstNonEmpty(msg.ErrorDetail.Message, msg.Error); m != "" {
			return fmt.Errorf("docker api: pulling %s: %s", ref, m)
		}
	}
}

// ImageInspect reports whether an image is present locally.
func (c *APIClient) ImageInspect(ctx context.Context, ref string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, nil)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// ImageIdentity resolves ref to the two immutable names an image has: the
// reference to create containers from, and the digest to record.
//
// For a pulled image the registry's manifest digest is what the UI shows and
// what "the same image as yesterday" means, but it is not a name the daemon
// resolves on its own: classic Docker looks a bare sha256: up as an image ID,
// which is the config digest, and answers "No such image". The repository
// digest reference -- name@sha256:manifest -- is resolvable everywhere, and is
// what containers are created from. An image the daemon built locally has no
// repository digest, and its ID serves as both.
func (c *APIClient) ImageIdentity(ctx context.Context, ref string) (createRef, digest string, err error) {
	var out struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &out); err != nil {
		return "", "", err
	}
	if len(out.RepoDigests) > 0 {
		if i := strings.LastIndex(out.RepoDigests[0], "@"); i >= 0 {
			return out.RepoDigests[0], out.RepoDigests[0][i+1:], nil
		}
	}
	return out.ID, out.ID, nil
}

// ContainerCreate creates a container and returns its ID.
func (c *APIClient) ContainerCreate(ctx context.Context, name string, cfg ContainerCreateRequest) (string, error) {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	var out struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/create", q, cfg, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// ContainerStart starts a created container.
func (c *APIClient) ContainerStart(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
}

// ContainerStop asks a container to stop, killing it after timeout. A container
// that has already stopped is not an error.
func (c *APIClient) ContainerStop(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{}
	secs := int(timeout.Round(time.Second) / time.Second)
	if secs < 0 {
		secs = 0
	}
	q.Set("t", strconv.Itoa(secs))

	// The daemon holds this request open until the container is down or the
	// grace period expires, so the default call deadline would cancel our own
	// stop on any timeout longer than a minute.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout+defaultCallTimeout)
		defer cancel()
	}
	err := c.do(ctx, http.MethodPost, "/containers/"+id+"/stop", q, nil, nil)
	if StatusCode(err) == http.StatusNotModified {
		return nil
	}
	return err
}

// ContainerKill signals a container. Signalling one that is not running is
// treated as success, since the caller's intent is already satisfied.
func (c *APIClient) ContainerKill(ctx context.Context, id, signal string) error {
	q := url.Values{}
	if signal != "" {
		q.Set("signal", signal)
	}
	err := c.do(ctx, http.MethodPost, "/containers/"+id+"/kill", q, nil, nil)
	if StatusCode(err) == http.StatusConflict {
		return nil
	}
	return err
}

// ContainerRemove deletes a container and its anonymous volumes.
func (c *APIClient) ContainerRemove(ctx context.Context, id string, force bool) error {
	q := url.Values{"v": {"1"}}
	if force {
		q.Set("force", "1")
	}
	return c.do(ctx, http.MethodDelete, "/containers/"+id, q, nil, nil)
}

// ContainerInspect returns the full state of one container.
func (c *APIClient) ContainerInspect(ctx context.Context, id string) (*ContainerInspect, error) {
	var out ContainerInspect
	if err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ContainerList lists containers, including stopped ones, matching filters such
// as {"label": {"io.zoomies.managed=true"}}.
func (c *APIClient) ContainerList(ctx context.Context, filters map[string][]string) ([]ContainerSummary, error) {
	q := url.Values{"all": {"1"}}
	if len(filters) > 0 {
		b, err := json.Marshal(filters)
		if err != nil {
			return nil, fmt.Errorf("docker api: encoding container filters: %w", err)
		}
		q.Set("filters", string(b))
	}
	var out []ContainerSummary
	if err := c.do(ctx, http.MethodGet, "/containers/json", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ContainerLogs streams a container's output. The caller closes the reader.
//
// Output is demultiplexed when the container has no TTY, which is the case for
// every container Zoomies creates.
func (c *APIClient) ContainerLogs(ctx context.Context, id string, opts LogQuery) (io.ReadCloser, error) {
	q := url.Values{}
	if opts.Stdout || (!opts.Stdout && !opts.Stderr) {
		q.Set("stdout", "1")
	}
	if opts.Stderr || (!opts.Stdout && !opts.Stderr) {
		q.Set("stderr", "1")
	}
	if opts.Follow {
		q.Set("follow", "1")
	}
	if opts.Timestamps {
		q.Set("timestamps", "1")
	}
	if opts.Tail > 0 {
		q.Set("tail", strconv.Itoa(opts.Tail))
	} else {
		q.Set("tail", "all")
	}
	if !opts.Since.IsZero() {
		q.Set("since", strconv.FormatInt(opts.Since.Unix(), 10))
	}

	// Whether the stream is framed depends on the container's TTY setting, and
	// the only way to know before reading is to ask. Inspect failures fall back
	// to "framed", which is what our own containers always are.
	tty := false
	if insp, err := c.ContainerInspect(ctx, id); err == nil && insp.Config != nil {
		tty = insp.Config.Tty
	} else if errors.Is(err, ErrNotFound) {
		return nil, err
	}

	resp, err := c.doRaw(ctx, http.MethodGet, "/containers/"+id+"/logs", q, nil, nil, true)
	if err != nil {
		return nil, err
	}
	// Since API 1.42 the daemon distinguishes the two framings by content type,
	// but only one of the values is worth anything. "multiplexed-stream" is
	// only ever sent for a framed stream, so it can override the inspect.
	// "raw-stream" cannot: it is what every daemon before 1.42 sends for both
	// framings, and what Podman's compatibility endpoint sends for a framed
	// stream today -- believing it left Docker's 8-byte frame headers in the
	// middle of every line of a downloaded runner log.
	if resp.Header.Get("Content-Type") == "application/vnd.docker.multiplexed-stream" {
		tty = false
	}
	if tty {
		return resp.Body, nil
	}
	return NewLogDemuxer(resp.Body), nil
}

// ContainerStats takes one resource sample.
//
// The request is stream=false but not one-shot: the daemon then collects two
// samples a moment apart, which is the only way it can report a CPU percentage.
func (c *APIClient) ContainerStats(ctx context.Context, id string) (StatsSample, error) {
	q := url.Values{"stream": {"false"}}
	var raw statsJSON
	if err := c.do(ctx, http.MethodGet, "/containers/"+id+"/stats", q, nil, &raw); err != nil {
		return StatsSample{}, err
	}
	return raw.sample(), nil
}

// NetworkEnsure creates a bridge network if it does not already exist.
func (c *APIClient) NetworkEnsure(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("docker api: a network name is required")
	}
	err := c.do(ctx, http.MethodGet, "/networks/"+name, nil, nil, nil)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	body := map[string]any{"Name": name, "Driver": "bridge", "CheckDuplicate": true}
	err = c.do(ctx, http.MethodPost, "/networks/create", nil, body, nil)
	// Another agent on this host may have won the race, which is fine.
	if StatusCode(err) == http.StatusConflict {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// Log demultiplexing
// ---------------------------------------------------------------------------

// logFrameHeader is the size of Docker's stream frame header: one byte of
// stream type, three of padding, then a big-endian uint32 payload length.
const logFrameHeader = 8

// LogDemuxer unwraps Docker's multiplexed stdout/stderr stream into a plain
// byte stream.
//
// Both streams are merged, in the order the daemon framed them, because a log
// viewer wants the interleaving the job actually produced. Frames routinely
// straddle Read boundaries, so the header is always read with io.ReadFull;
// getting that wrong silently corrupts the viewer rather than failing, which is
// why it is separated out and tested.
type LogDemuxer struct {
	r    io.Reader
	c    io.Closer
	hdr  [logFrameHeader]byte
	left uint32
}

// NewLogDemuxer wraps a framed log stream.
func NewLogDemuxer(rc io.ReadCloser) *LogDemuxer {
	return &LogDemuxer{r: bufio.NewReader(rc), c: rc}
}

func (d *LogDemuxer) Read(p []byte) (int, error) {
	for d.left == 0 {
		if _, err := io.ReadFull(d.r, d.hdr[:]); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				// A truncated header means the daemon or the connection cut the
				// stream mid-frame. Report the end rather than the corruption.
				return 0, io.EOF
			}
			return 0, err
		}
		d.left = binary.BigEndian.Uint32(d.hdr[4:])
	}
	if uint64(len(p)) > uint64(d.left) {
		p = p[:d.left]
	}
	n, err := d.r.Read(p)
	d.left -= uint32(n)
	if errors.Is(err, io.EOF) && n > 0 {
		err = nil
	}
	return n, err
}

// Close releases the underlying stream.
func (d *LogDemuxer) Close() error {
	if d.c == nil {
		return nil
	}
	return d.c.Close()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// statsJSON is the subset of the stats payload needed to compute a sample.
type statsJSON struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PerCPUUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

func (s statsJSON) sample() StatsSample {
	out := StatsSample{MemoryLimit: int64(s.MemoryStats.Limit)}

	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemCPUUsage) - float64(s.PreCPUStats.SystemCPUUsage)
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PerCPUUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	if cpuDelta > 0 && sysDelta > 0 {
		out.CPUPercent = cpuDelta / sysDelta * cpus * 100
	}

	// The daemon's "usage" includes page cache, which makes a build look like it
	// is about to be OOM-killed when it has merely read a lot of files. Docker's
	// own CLI subtracts the inactive file cache, so we do too.
	mem := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < mem {
		mem -= v
	} else if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok && v < mem {
		mem -= v
	} else if v, ok := s.MemoryStats.Stats["cache"]; ok && v < mem {
		mem -= v
	}
	out.MemoryBytes = int64(mem)
	return out
}

// splitImageRef separates an image reference into the name the API wants in
// fromImage and the tag or digest it wants separately. A registry host with a
// port ("registry:5000/img") must not be mistaken for a tag.
func splitImageRef(ref string) (name, tag string) {
	if i := strings.LastIndex(ref, "@"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 {
		return ref, "latest"
	}
	if strings.Contains(ref[i+1:], "/") {
		return ref, "latest"
	}
	return ref[:i], ref[i+1:]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseDockerTime reads one of the daemon's timestamps, returning the zero time
// for "never" (which Docker spells 0001-01-01T00:00:00Z) and for anything
// unparseable, since a missing timestamp must not fail a status call.
func parseDockerTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t
}
