package proxmox

// This file is a small, hand-written client for the Proxmox VE API.
//
// Why not a Proxmox SDK? The same argument internal/backend/dockerapi.go makes
// about github.com/docker/docker, with a weaker case for the dependency: this
// integration touches about a dozen endpoints, there is no pagination, there is
// one error shape, and no second consumer to amortise a vendored SDK over.
// Zoomies is a single binary an operator downloads and runs, so its dependency
// tree is part of its user interface -- every module in it is something they
// have to trust, audit and rebuild when a CVE lands. The API is a documented
// REST interface over HTTPS with one authentication header, so speaking it with
// net/http costs one file.
//
// The parts that are genuinely fiddly are implemented here once and unit
// tested: the task handle (upid.go), the wire's habit of writing numbers as
// strings and booleans as 0 or 1, and -- the one that decides whether this
// fleet pays for a machine nobody is tracking -- telling a request that never
// left from one whose answer never came back.

import (
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
	"net/http/httptrace"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/version"
)

// APIPath is the prefix every Proxmox VE endpoint sits behind. Every response
// under it is an object with one "data" member, which is why it is unwrapped in
// exactly one place below.
const APIPath = "/api2/json"

// DefaultPort is the port a Proxmox VE node serves its API on. It is appended
// when an operator writes a bare hostname, because "pve-1.example.com" is what
// they call the machine and :8006 is a detail of ours.
const DefaultPort = "8006"

// MinPVEVersion is the oldest Proxmox VE this integration is qualified against.
//
// It is pinned rather than inferred because "qualified" is a claim about
// evidence, not about whether a call happened to work: 8.0 is the release the
// endpoints used here were checked against, and the one the runbook's
// qualification record names. An older cluster is warned about rather than
// refused -- refusing would strand an operator whose 7.4 cluster may well work,
// and the warning is what tells them nobody has checked.
const MinPVEVersion = "8.0"

// Timeouts for the request/response path. There is deliberately no
// http.Client.Timeout: a client-wide timeout cannot tell a slow clone from a
// followed task log, so every call puts its own deadline on the context and
// this is only the floor under the parts of a request the context cannot see.
const (
	dialTimeout           = 5 * time.Second
	responseHeaderTimeout = 30 * time.Second
	defaultCallTimeout    = 60 * time.Second
)

// maxErrorBody is how much of a failure's body is read before it is discarded.
// A proxy in front of the cluster can answer with a page rather than a
// sentence, and an error message is not a place to accumulate one.
const maxErrorBody = 64 << 10

// Options is what a client needs. None of it is read from a file here: the
// endpoint and the credential are a stored provider row, unsealed by the
// controller for the life of the call that builds this.
type Options struct {
	// Endpoint is the cluster as the operator wrote it: "https://pve-1:8006",
	// "pve-1.example.com", or a hostname with the API path already on it.
	Endpoint string
	// TokenID is "user@realm!tokenid". It is not a secret -- it names the
	// token in every message that asks for a privilege to be granted.
	TokenID string
	// Secret is the token's secret, shown once by Proxmox when it was created.
	// It is held for the life of this client, never logged and never written.
	Secret string
	// CAPEM replaces the system root pool when set. For the usual cluster this
	// is /etc/pve/pve-root-ca.pem -- certificate pinning, not a public CA.
	CAPEM string
	// Insecure turns verification off entirely. It is carried rather than
	// refused because a homelab cluster's certificate is usually its own, and
	// every construction with it set says what it costs.
	Insecure bool
	// DialContext, when set, opens every connection instead of the network,
	// and no proxy is consulted. It is how a cluster behind a private
	// connection is reached; the endpoint's host is then only the name the
	// certificate is checked against.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
	// HTTPClient replaces the client's own. Tests use it; production leaves it
	// nil so that the TLS settings above and DialContext take effect.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Client talks to one Proxmox VE cluster.
//
// It is safe for concurrent use; the embedded http.Client pools connections.
// Nothing here retries and nothing here reads a clock for its own decisions:
// backoff belongs to the reconciler, as it does in internal/github.
type Client struct {
	// endpoint is the cluster as the operator wrote it, so a message names the
	// thing in their configuration rather than a URL we assembled.
	endpoint string
	base     string
	tokenID  string
	// auth is the whole Authorization header value, assembled once. It carries
	// the secret, so it is never logged and never put in an error.
	auth     string
	insecure bool
	http     *http.Client
	log      *slog.Logger
}

// New builds a client for one cluster.
//
// It never dials. Reachability is Preflight's answer to give, for the reason
// backend.Probe reports an absent Docker rather than failing to construct: a
// controller that would not start because a hypervisor was rebooting could not
// even show an operator why.
func New(opts Options) (*Client, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "provider.proxmox")

	u, err := parseEndpoint(opts.Endpoint)
	if err != nil {
		return nil, err
	}
	auth, err := authorisation(opts.TokenID, opts.Secret)
	if err != nil {
		return nil, err
	}

	c := &Client{
		endpoint: strings.TrimSpace(opts.Endpoint),
		base:     strings.TrimSuffix(u.String(), "/") + APIPath,
		tokenID:  strings.TrimSpace(opts.TokenID),
		auth:     auth,
		insecure: opts.Insecure,
		log:      log,
	}

	if opts.HTTPClient != nil {
		c.http = opts.HTTPClient
		return c, nil
	}
	tlsCfg, err := buildTLSConfig(opts, u, log)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		TLSClientConfig:       tlsCfg,
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if opts.DialContext != nil {
		// A proxy would take the connection out of the private tunnel, and
		// the address it would be sent to does not exist from here anyway.
		transport.DialContext = opts.DialContext
		transport.Proxy = nil
	}
	c.http = &http.Client{
		// No Client.Timeout on purpose: a deadline that applies to every call
		// equally is either too short for a clone or too long for a status
		// read, so each call sets its own on the context instead.
		Transport: transport,
	}
	return c, nil
}

// Endpoint returns the cluster address this client was built from.
func (c *Client) Endpoint() string { return c.endpoint }

// TokenID returns the token's name, which is not a secret and is what a message
// asking for a privilege has to quote.
func (c *Client) TokenID() string { return c.tokenID }

// parseEndpoint turns what an operator typed into a base URL.
//
// A plain http:// endpoint that is not on loopback is refused outright, on the
// same terms as agent.allow_insecure_http and for a worse reason: this header
// is a credential that can clone, start and destroy virtual machines on a
// hypervisor, and there is no setting here that permits sending it in the
// clear.
func parseEndpoint(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("proxmox: no endpoint; give the cluster's address, for example https://pve-1.example.com:8006")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("proxmox: %q is not an address; use something like https://pve-1.example.com:8006: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("proxmox: refusing to talk to %s over plain HTTP: this token can clone, start and destroy "+
				"virtual machines, and over http:// it crosses the network in the clear on every request. "+
				"Use https:// -- a Proxmox node serves it on port %s with its own certificate, and ca_pem pins it", raw, DefaultPort)
		}
	default:
		return nil, fmt.Errorf("proxmox: endpoint %q must start with https://", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxmox: endpoint %q names no host; use something like https://pve-1.example.com:8006", raw)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), DefaultPort)
	}
	// An operator who pasted the address out of a browser or a curl line brings
	// the API path with it, and joining it twice would 404 every call with a
	// message about a path they never typed.
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), APIPath)
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

// isLoopbackHost reports whether an address names this machine, which is the
// one case where plain HTTP carries nothing off the box.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authorisation builds the one header Proxmox needs.
//
// An API token authenticates with a single header and nothing else: no ticket,
// no CSRF header, no login round trip. That is worth saying out loud, because
// it means there is no cached credential to refresh and therefore none of the
// "an authentication failure arrives from inside the transport, unclassified"
// trap that a ticket-based client has to work around.
func authorisation(tokenID, secret string) (string, error) {
	id, sec := strings.TrimSpace(tokenID), strings.TrimSpace(secret)
	if id == "" {
		return "", errors.New("proxmox: no API token id; it has the form user@realm!tokenid, for example zoomies@pve!fleet")
	}
	if strings.Contains(id, "=") {
		return "", fmt.Errorf("proxmox: the API token id %q contains \"=\", so it looks like the id and the secret pasted together; "+
			"give the part before the \"=\" as the token id and the part after it as the secret", id)
	}
	if !strings.Contains(id, "@") || !strings.Contains(id, "!") {
		return "", fmt.Errorf("proxmox: %q is not a Proxmox token id; it has the form user@realm!tokenid, for example zoomies@pve!fleet", id)
	}
	if sec == "" {
		return "", errors.New("proxmox: no API token secret; it is the UUID Proxmox showed once when the token was created -- if it was not kept, make a new token")
	}
	return "PVEAPIToken=" + id + "=" + sec, nil
}

// buildTLSConfig turns the certificate settings into a tls.Config, failing
// early with a message that names the setting at fault.
func buildTLSConfig(opts Options, u *url.URL, log *slog.Logger) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if opts.Insecure {
		// Warned at every construction rather than once at startup, because the
		// failure mode is silent: anything able to intercept this connection
		// holds a credential that can destroy virtual machines, and nothing
		// else in the system will ever mention it again.
		log.Warn("Proxmox certificate verification is disabled; anything able to intercept this connection can use this token to create and destroy virtual machines",
			"setting", "insecure_skip_verify",
			"endpoint", u.Redacted(),
			"fix", "paste the cluster's CA certificate from /etc/pve/pve-root-ca.pem into ca_pem instead")
		cfg.InsecureSkipVerify = true
	}

	if pem := strings.TrimSpace(opts.CAPEM); pem != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			hint := ""
			if strings.Contains(pem, "PRIVATE KEY") {
				hint = " What is there is a private key -- that is the certificate's key, not the certificate, and it must not leave the node."
			}
			return nil, fmt.Errorf("proxmox: ca_pem holds no PEM certificate.%s Copy the cluster's certificate authority from "+
				"/etc/pve/pve-root-ca.pem on any node; it begins with -----BEGIN CERTIFICATE-----", hint)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// APIError is a non-2xx answer, carrying Proxmox's own explanation so an
// operator reads "storage 'local-lvm' does not exist" rather than "status 500".
// It is the Cause inside a provider.Error, never the classification itself:
// what the reconciler does next is decided once, here, and never rediscovered
// from a string higher up.
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
	return fmt.Sprintf("proxmox api: %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// StatusCode returns the HTTP status an error carries, or 0 when it did not
// come from the cluster at all.
//
// It reaches through provider.Error.Cause by hand because that type unwraps to
// its category sentinel rather than to what went wrong underneath -- on
// purpose, so that nothing rediscovers a classification from the cause. The
// status is still worth having for a log line and a test.
func StatusCode(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	var pe *provider.Error
	if errors.As(err, &pe) && pe.Cause != nil {
		return StatusCode(pe.Cause)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Request plumbing
// ---------------------------------------------------------------------------

// call is one request: what is being attempted, in prose written at the call
// site, and the wire details. The prose is the call site's because "clone
// template 9000 to 143 on pve-1" is a sentence only the caller can write, and
// it is what an operator reads when the step fails.
type call struct {
	op    string
	ref   string
	verb  string
	path  string
	query url.Values
	form  url.Values
}

// mutating reports whether this call could have changed something at the
// cluster. It is what makes a lost answer either an unknown outcome or merely
// a failed read, and it is decided by the method rather than by the path
// because a new endpoint must not be able to join the safe side by accident.
func (cl call) mutating() bool { return cl.verb != http.MethodGet }

func (c *Client) urlFor(path string, q url.Values) string {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// do performs one call and unwraps Proxmox's {"data": ...} envelope into out,
// which may be nil when the answer is not wanted.
func (c *Client) do(ctx context.Context, cl call, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}

	var body io.Reader
	if len(cl.form) > 0 {
		body = strings.NewReader(cl.form.Encode())
	}

	// Whether the request actually went out is the one fact the layers above
	// cannot recover, and the whole ambiguity classification rests on it, so it
	// is taken from the transport itself rather than guessed from the error.
	var sent atomic.Bool
	traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				sent.Store(true)
			}
		},
	})

	req, err := http.NewRequestWithContext(traced, cl.verb, c.urlFor(cl.path, cl.query), body)
	if err != nil {
		return &provider.Error{Kind: provider.FailureInternal, Op: cl.op, Ref: cl.ref,
			Message: "the request could not be built", Cause: err}
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return c.classify(cl, nil, err, sent.Load())
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return c.classify(cl, resp, nil, true)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}

	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		// The answer to a call we know went out was not readable. For a mutating
		// call that is an unknown outcome and not a failure: the clone may well
		// be running, and a retry is how one machine becomes two.
		return c.classify(cl, nil, err, true)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return &provider.Error{Kind: provider.FailureInternal, Op: cl.op, Ref: cl.ref,
			Message: fmt.Sprintf("the answer to %s %s was not the shape this build expects", cl.verb, cl.path), Cause: err}
	}
	return nil
}

// classify turns one call's outcome into the category the reconciler decides
// on. It is the most important function in this package.
//
// The order matters, and the second rule is the one the fleet's money rests on:
// a deadline or a reset AFTER the request went out means we asked a hypervisor
// to do something and never learned whether it did, which is ambiguous and must
// never be retried blindly; before it, nothing was asked and the cluster is
// merely unreachable. Only the transport knows which of the two happened, so
// only the transport may decide -- get it wrong in the unsafe direction and the
// fleet rents a second machine and pays for the one nobody is tracking.
func (c *Client) classify(cl call, resp *http.Response, err error, sentRequest bool) error {
	if err != nil {
		// A cancelled context is the caller's own doing, not the cluster's, and
		// a caller that is shutting down must not see its own stop reported as
		// an unknown outcome.
		if errors.Is(err, context.Canceled) {
			return err
		}
		if sentRequest && cl.mutating() {
			return &provider.Error{
				Kind: provider.FailureAmbiguous, Op: cl.op, Ref: cl.ref,
				Message: fmt.Sprintf("the request reached %s and no answer came back, so whether it happened is unknown", c.endpoint),
				Remedy:  "nothing may be created or deleted on the strength of this; the machine is reconciled by identity before anything else is attempted",
				Cause:   err,
			}
		}
		// A read that never got its answer changed nothing at the cluster, so
		// it is a reachability problem and may simply be tried again.
		return &provider.Error{Kind: provider.FailureUnreachable, Op: cl.op, Ref: cl.ref,
			Message: unreachable(c.endpoint, err), Remedy: unreachableRemedy(c.endpoint, err), Cause: err}
	}

	msg := decodeMessage(resp)
	api := &APIError{Status: resp.StatusCode, Method: cl.verb, Path: cl.path, Message: msg}
	fail := func(kind provider.FailureKind, remedy string) error {
		return &provider.Error{Kind: kind, Op: cl.op, Ref: cl.ref, Message: msg, Remedy: remedy,
			RetryAfter: retryAfter(resp), Cause: api}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fail(provider.FailureAuth, fmt.Sprintf("check the API token %q and its secret; a secret is shown once when the token is created, "+
			"and a token whose secret was not kept has to be replaced", c.tokenID))
	case resp.StatusCode == http.StatusForbidden:
		privilege, path := privilegeFrom(msg)
		if privilege == "" {
			return fail(provider.FailurePermission, fmt.Sprintf("grant the API token %q the privilege this call needs: "+
				"Datacenter -> Permissions -> API Tokens", c.tokenID))
		}
		return fail(provider.FailurePermission, fmt.Sprintf("grant %s on %s to the API token %q: pveum acl modify %s --tokens '%s' --roles <role with %s>",
			privilege, path, c.tokenID, path, c.tokenID, privilege))
	case resp.StatusCode == http.StatusNotFound:
		return fail(provider.FailureNotFound, "")
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusNotImplemented:
		return fail(provider.FailureConfig, "the cluster refused the parameters of this call; the provider's settings are what decide them")
	case looksLikeQuota(msg):
		// Proxmox has no machine-readable code for "no room", so its own words
		// are all there is to go on. Getting this wrong costs a slow retry;
		// not trying would report a full datastore as an unexplained refusal.
		return fail(provider.FailureQuota, "free space on the storage this provider clones into, or point it at another one")
	case looksLikeConflict(msg):
		return fail(provider.FailureConflict, "something else is holding this guest; it is waited for rather than forced")
	case resp.StatusCode >= 500 && cl.mutating():
		return fail(provider.FailureAmbiguous, "nothing may be created or deleted on the strength of this; the machine is reconciled by identity first")
	case resp.StatusCode >= 500:
		return fail(provider.FailureRefused, "")
	default:
		return fail(provider.FailureRefused, "")
	}
}

// unreachable turns a transport failure into a sentence that names the fix,
// because "connect: connection refused" on its own has sent many operators to
// the wrong place.
func unreachable(endpoint string, err error) string {
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	switch {
	case isLoopback(endpoint) && (errors.As(err, &unknown) || errors.As(err, &hostname)):
		// The commonest way to land here is a controller on the Proxmox host
		// itself, pointed at localhost. Proxmox issues its certificate for the
		// node's own name and address, so no CA, however it is pinned, will ever
		// vouch for "localhost"; saying only "not trusted" sends people to paste
		// a CA that cannot help.
		return fmt.Sprintf("the certificate %s presented was issued for the node's own name, not for localhost, so it cannot be verified", endpoint)
	case errors.As(err, &unknown):
		return fmt.Sprintf("the certificate %s presented is not signed by an authority this controller trusts", endpoint)
	case errors.As(err, &hostname):
		return fmt.Sprintf("the certificate %s presented is for another name: %s", endpoint, hostname.Error())
	case errors.As(err, &dns):
		return fmt.Sprintf("%s does not resolve", dns.Name)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Sprintf("nothing is listening at %s", endpoint)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("%s did not answer within the deadline for this call", endpoint)
	default:
		return fmt.Sprintf("%s could not be reached: %v", endpoint, err)
	}
}

// isLoopback reports whether the endpoint names this machine, by name or by
// loopback address.
func isLoopback(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// unreachableRemedy is the next action for the failures that have one.
//
// Where the certificate is the problem, the last resort is named too, with
// what it costs: an operator who has to turn verification off should do it
// knowingly, from the message, not by guessing at the setting.
func unreachableRemedy(endpoint string, err error) string {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	const accept = "or, to accept the risk, set insecure_skip_verify: the API token is then sent to whatever answers at the endpoint, " +
		"so anything able to intercept the connection can use it to create and destroy virtual machines"
	switch {
	case isLoopback(endpoint) && (errors.As(err, &unknown) || errors.As(err, &hostname)):
		return "set endpoint to the node's real name or IP address (the one on its certificate) and paste the cluster's CA certificate from " +
			"/etc/pve/pve-root-ca.pem into ca_pem; " + accept
	case errors.As(err, &unknown), errors.As(err, &hostname):
		return "paste the cluster's CA certificate from /etc/pve/pve-root-ca.pem into ca_pem, or use a certificate whose name matches the endpoint; " + accept
	case errors.Is(err, syscall.ECONNREFUSED):
		return "check the node is up and that the API is on port " + DefaultPort
	default:
		return ""
	}
}

// decodeMessage pulls Proxmox's own explanation out of a failure.
//
// It looks in three places because Proxmox uses all of them: the "errors" map a
// parameter check fills in, a "message" member, and the HTTP status line, where
// the sentence about a missing storage or a locked guest usually ends up. The
// raw body is the last resort so a proxy's answer is not lost, and every path
// is capped, because an error message is not a place to reprint somebody's HTML.
func decodeMessage(resp *http.Response) string {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err == nil && len(b) > 0 {
		var env struct {
			Errors  map[string]string `json:"errors"`
			Message string            `json:"message"`
		}
		if json.Unmarshal(b, &env) == nil {
			if len(env.Errors) > 0 {
				keys := make([]string, 0, len(env.Errors))
				for k := range env.Errors {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				parts := make([]string, 0, len(keys))
				for _, k := range keys {
					parts = append(parts, k+": "+env.Errors[k])
				}
				return trimMessage(strings.Join(parts, "; "))
			}
			if env.Message != "" {
				return trimMessage(env.Message)
			}
		}
	}
	if reason := statusReason(resp); reason != "" {
		return reason
	}
	if text := strings.TrimSpace(string(b)); text != "" && text != `{"data":null}` {
		return trimMessage(text)
	}
	return ""
}

// statusReason is the part of the status line after the code. Proxmox writes
// its complaint there -- "500 storage 'local-lvm' does not exist" -- and a
// client that only read the body would throw away the only sentence it sent.
func statusReason(resp *http.Response) string {
	_, reason, ok := strings.Cut(strings.TrimSpace(resp.Status), " ")
	if !ok {
		return ""
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || reason == http.StatusText(resp.StatusCode) {
		return ""
	}
	return trimMessage(reason)
}

func trimMessage(s string) string {
	const max = 512
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// retryAfter reads a Retry-After header when there is one. Proxmox rarely sends
// it, but a reverse proxy or a rate limiter in front of the cluster does, and a
// number the far side gave us beats a guess of ours.
func retryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// privilegePattern matches a Proxmox privilege as it appears in a refusal --
// "Permission check failed (/vms/9000, VM.Clone)" -- so the remedy can name the
// privilege and the path rather than telling an operator to go and find out
// which of eleven it was.
var privilegePattern = regexp.MustCompile(`([A-Z][A-Za-z]+(?:\.[A-Za-z]+)+)`)

// pathPattern matches the object path in the same sentence.
var pathPattern = regexp.MustCompile(`(/[A-Za-z0-9_./-]*)`)

func privilegeFrom(msg string) (privilege, path string) {
	if m := privilegePattern.FindStringSubmatch(msg); m != nil {
		privilege = m[1]
	}
	if m := pathPattern.FindStringSubmatch(msg); m != nil {
		path = strings.TrimSuffix(m[1], ",")
	}
	if path == "" {
		path = "/vms"
	}
	return privilege, path
}

// quotaWords are the ways Proxmox says "there is no room". There is no code to
// match on, so these are the evidence; each one is a refusal that waiting or
// freeing space fixes, and none of them is fixed by asking again immediately.
var quotaWords = []string{
	"no space left",
	"not enough space",
	"insufficient space",
	"is full",
	"quota",
	"out of memory",
	"cannot allocate memory",
	"no such free",
	"limit reached",
	"exceeds",
	"maximum number",
}

func looksLikeQuota(msg string) bool { return containsAny(strings.ToLower(msg), quotaWords) }

// conflictWords are the ways Proxmox says "something else has this". A lock is
// another operation in progress, and an identifier already taken is another
// caller having won the race for it -- both are resolved by looking again, not
// by forcing.
var conflictWords = []string{
	"lock",
	"already exists",
	"already running",
	"in use",
	"is busy",
}

func looksLikeConflict(msg string) bool { return containsAny(strings.ToLower(msg), conflictWords) }

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Wire types
//
// Proxmox's JSON is generated from Perl, and it shows: the same field arrives
// as a number from one endpoint and as a string from another, and every boolean
// is 0 or 1. Every field below that lies carries a comment saying so, because
// the alternative is a future reader "tidying" the type back to int and finding
// out from a live cluster.
// ---------------------------------------------------------------------------

// Int is an integer Proxmox may write as a JSON number or as a decimal string.
// vmid is a number in /cluster/resources and a string in a task's fields;
// memory sizes occasionally arrive in exponential notation.
type Int int64

func (n *Int) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*n = Int(v)
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("proxmox: %q is not a number", s)
	}
	*n = Int(v)
	return nil
}

// Int returns the value as a plain int for the callers that do arithmetic.
func (n Int) Int() int { return int(n) }

// Bool is a flag Proxmox writes as 0 or 1, sometimes as "0" or "1", and
// sometimes -- in the same document -- as a real JSON boolean.
type Bool bool

func (b *Bool) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	switch s {
	case "", "null", "0", "false":
		*b = false
		return nil
	case "1", "true":
		*b = true
		return nil
	}
	// Anything else is a value this build has not seen. Reading it as false is
	// the safe direction everywhere it is used: an unrecognised "template" flag
	// makes a guest look like a machine rather than like a template, and a
	// machine is the thing nothing deletes without further proof.
	*b = false
	return nil
}

// VersionInfo is GET /version, the cheap reachability and credential probe.
type VersionInfo struct {
	Version string `json:"version"` // "8.2.4"
	Release string `json:"release"`
	RepoID  string `json:"repoid"`
}

// Permissions is GET /access/permissions: an object path, the privileges held
// on it, and whether each one propagates to the paths beneath.
type Permissions map[string]map[string]Bool

// UnmarshalJSON tolerates the empty list Proxmox sends for an empty map. It is
// Perl's doing -- an empty hash serialises as [] -- and a token with no
// privileges at all is exactly the case preflight exists to explain, so it must
// not arrive as a decode error.
func (p *Permissions) UnmarshalJSON(b []byte) error {
	if strings.TrimSpace(string(b)) == "[]" {
		*p = Permissions{}
		return nil
	}
	var m map[string]map[string]Bool
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*p = m
	return nil
}

// Node is one entry of GET /nodes.
type Node struct {
	Node   string  `json:"node"`
	Status string  `json:"status"` // "online", "offline" or "unknown"
	CPU    float64 `json:"cpu"`
	MaxCPU Int     `json:"maxcpu"`
	Mem    Int     `json:"mem"`    // bytes, and a string on some releases
	MaxMem Int     `json:"maxmem"` // bytes, ditto
	Uptime Int     `json:"uptime"`
}

// Online reports whether this node can be placed on.
func (n Node) Online() bool { return n.Status == "online" }

// Storage is one entry of GET /nodes/{node}/storage.
type Storage struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	// Content is a comma-separated list -- "images,rootdir" -- not an array.
	Content string `json:"content"`
	Enabled Bool   `json:"enabled"` // 0 or 1
	Active  Bool   `json:"active"`  // 0 or 1
	Shared  Bool   `json:"shared"`  // 0 or 1
	Avail   Int    `json:"avail"`   // bytes
	Total   Int    `json:"total"`   // bytes
}

// Accepts reports whether this storage holds a content type, which is what
// decides whether a clone's disk can land on it at all.
func (s Storage) Accepts(content string) bool {
	for _, c := range strings.Split(s.Content, ",") {
		if strings.EqualFold(strings.TrimSpace(c), content) {
			return true
		}
	}
	return false
}

// NetworkInterface is one entry of GET /nodes/{node}/network.
type NetworkInterface struct {
	Iface     string `json:"iface"`
	Type      string `json:"type"` // "bridge" for the ones a guest attaches to
	Active    Bool   `json:"active"`
	Autostart Bool   `json:"autostart"`
	CIDR      string `json:"cidr"`
	Comments  string `json:"comments"`
}

// ClusterVM is one guest from GET /cluster/resources?type=vm.
//
// That call answers for every node at once, which is what makes the ownership
// sweep one request rather than one per node, and it is the only place tags are
// readable without asking each guest in turn.
type ClusterVM struct {
	VMID     Int    `json:"vmid"` // a number here, a string in a task's fields
	Node     string `json:"node"`
	Name     string `json:"name"`
	Status   string `json:"status"` // "running" or "stopped"
	Type     string `json:"type"`   // "qemu" or "lxc"
	Template Bool   `json:"template"`
	// Tags is semicolon-separated, and absent rather than empty when a guest
	// has none.
	Tags   string `json:"tags"`
	Pool   string `json:"pool"`
	MaxMem Int    `json:"maxmem"`
	MaxCPU Int    `json:"maxcpu"`
	Uptime Int    `json:"uptime"`
}

// VMStatus is GET /nodes/{node}/qemu/{vmid}/status/current.
type VMStatus struct {
	VMID      Int    `json:"vmid"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	QMPStatus string `json:"qmpstatus"`
	// Lock is the operation holding the guest -- "clone", "migrate", "backup".
	// Empty means nothing holds it.
	Lock     string `json:"lock"`
	Template Bool   `json:"template"`
	Tags     string `json:"tags"`
	Uptime   Int    `json:"uptime"`
	Agent    Bool   `json:"agent"`
}

// VMConfig is GET /nodes/{node}/qemu/{vmid}/config: the recorded configuration,
// and the authoritative place to read back the description and the tags this
// fleet stamped on a guest.
type VMConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tags        string `json:"tags"`
	Template    Bool   `json:"template"`
	// Agent is a property string -- "1", or "enabled=1,fstrim_cloned_disks=1" --
	// rather than a flag, because it carries options.
	Agent   string `json:"agent"`
	Cores   Int    `json:"cores"`
	Sockets Int    `json:"sockets"`
	Memory  Int    `json:"memory"` // mebibytes, and a string on some releases
	Net0    string `json:"net0"`
	SMBIOS1 string `json:"smbios1"`
	SCSI0   string `json:"scsi0"`
	VirtIO0 string `json:"virtio0"`
	SATA0   string `json:"sata0"`
	IDE0    string `json:"ide0"`
}

// HasDisk reports whether the guest has an operating-system disk on one of the
// four first slots a cloud image is attached to. A CD-ROM or a cloud-init drive
// in one of them does not count: neither can be booted into a machine.
//
// It looks at slot zero only, which is where every runbook puts the image, so a
// template that keeps its disk elsewhere would be reported here wrongly; the
// finding names the slots it looked at for that reason.
func (c VMConfig) HasDisk() bool {
	for _, spec := range []string{c.SCSI0, c.VirtIO0, c.SATA0, c.IDE0} {
		spec = strings.TrimSpace(spec)
		if spec == "" || strings.Contains(spec, "media=cdrom") || strings.Contains(spec, "cloudinit") {
			continue
		}
		return true
	}
	return false
}

// AgentEnabled reports whether the guest agent is switched on for this guest,
// which the qualified bootstrap needs and a template is easy to forget it in.
func (c VMConfig) AgentEnabled() bool {
	for _, part := range strings.Split(c.Agent, ",") {
		part = strings.TrimSpace(part)
		switch part {
		case "1", "enabled=1":
			return true
		}
	}
	return false
}

// TaskStatus is GET /nodes/{node}/tasks/{upid}/status.
type TaskStatus struct {
	UPID   string `json:"upid"`
	Node   string `json:"node"`
	Type   string `json:"type"`
	Status string `json:"status"` // "running" or "stopped"
	// ExitStatus is only set once the task has stopped. "OK" is success and
	// anything else is the failure, in Proxmox's own words.
	ExitStatus string `json:"exitstatus"`
	ID         string `json:"id"`
	PID        Int    `json:"pid"`
	StartTime  Int    `json:"starttime"`
}

// Done reports whether the task has finished, whatever the outcome.
func (t TaskStatus) Done() bool { return t.Status == "stopped" }

// OK reports whether it finished and worked. The comparison is exact: Proxmox
// writes "OK" for success and the reason for everything else, warnings
// included, and a task that ended "WARNINGS: 1" has not done what was asked.
func (t TaskStatus) OK() bool { return t.Done() && t.ExitStatus == "OK" }

// AgentExecStatus is GET /nodes/{node}/qemu/{vmid}/agent/exec-status.
//
// It is where the guest's own account of a failed bootstrap comes from, which
// is the only sentence that ever says why the agent did not install.
type AgentExecStatus struct {
	Exited   Bool   `json:"exited"` // 0 or 1
	ExitCode Int    `json:"exitcode"`
	OutData  string `json:"out-data"`
	ErrData  string `json:"err-data"`
	Signal   Int    `json:"signal"`
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// Version reads the cluster's version. It is the cheap probe: any credential
// may call it, so a failure separates "cannot be reached" from "was refused".
func (c *Client) Version(ctx context.Context) (VersionInfo, error) {
	var v VersionInfo
	err := c.do(ctx, call{op: "read the Proxmox version", verb: http.MethodGet, path: "/version"}, &v)
	return v, err
}

// Permissions asks what this token may do -- the token asking about itself,
// which is what turns "check the prerequisites" into a real check.
func (c *Client) Permissions(ctx context.Context) (Permissions, error) {
	var p Permissions
	err := c.do(ctx, call{op: "read the API token's own permissions", verb: http.MethodGet, path: "/access/permissions"}, &p)
	if p == nil {
		p = Permissions{}
	}
	return p, err
}

// Nodes lists the cluster's nodes.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var out []Node
	err := c.do(ctx, call{op: "list the cluster's nodes", verb: http.MethodGet, path: "/nodes"}, &out)
	return out, err
}

// Storages lists the storages a node can see.
func (c *Client) Storages(ctx context.Context, node string) ([]Storage, error) {
	var out []Storage
	err := c.do(ctx, call{op: "list the storages on " + node, ref: node, verb: http.MethodGet,
		path: "/nodes/" + url.PathEscape(node) + "/storage"}, &out)
	return out, err
}

// Bridges lists the network bridges on a node -- the ones a guest's interface
// can actually be attached to.
func (c *Client) Bridges(ctx context.Context, node string) ([]NetworkInterface, error) {
	path := "/nodes/" + url.PathEscape(node) + "/network"
	var out []NetworkInterface
	err := c.do(ctx, call{op: "list the network bridges on " + node, ref: node, verb: http.MethodGet,
		path: path, query: url.Values{"type": {"any_bridge"}}}, &out)
	if err != nil || len(out) > 0 {
		return out, err
	}
	// Proxmox answers any_bridge for a restricted API token with whatever that
	// token may attach a guest to, which can be nothing even where the node has
	// a perfectly good vmbr0 that root sees. The unfiltered list is the node's
	// own inventory, so ask for that and pick the bridges out here rather than
	// reporting a bridge missing that is plainly there.
	var all []NetworkInterface
	if err := c.do(ctx, call{op: "list the network interfaces on " + node, ref: node, verb: http.MethodGet, path: path}, &all); err != nil {
		return out, nil
	}
	for _, n := range all {
		if n.Type == "bridge" || n.Type == "OVSBridge" {
			out = append(out, n)
		}
	}
	return out, nil
}

// ClusterVMs lists every guest on every node in one call.
//
// This is the ownership sweep. Iterating nodes would be one request per node
// per pass, would miss a guest on a node that is briefly unreachable, and would
// make the sweep's cost grow with the cluster; this call does not.
func (c *Client) ClusterVMs(ctx context.Context) ([]ClusterVM, error) {
	var out []ClusterVM
	err := c.do(ctx, call{op: "list every guest in the cluster", verb: http.MethodGet,
		path: "/cluster/resources", query: url.Values{"type": {"vm"}}}, &out)
	return out, err
}

// VMStatus reads one guest's current state, including the lock that says
// another operation owns it.
func (c *Client) VMStatus(ctx context.Context, node string, vmid int) (VMStatus, error) {
	var s VMStatus
	err := c.do(ctx, call{op: fmt.Sprintf("read the status of VM %d on %s", vmid, node), ref: vmRef(node, vmid),
		verb: http.MethodGet, path: vmPath(node, vmid) + "/status/current"}, &s)
	return s, err
}

// VMConfig reads one guest's recorded configuration, which is where the
// ownership block written into the description is read back from.
func (c *Client) VMConfig(ctx context.Context, node string, vmid int) (VMConfig, error) {
	var cfg VMConfig
	err := c.do(ctx, call{op: fmt.Sprintf("read the configuration of VM %d on %s", vmid, node), ref: vmRef(node, vmid),
		verb: http.MethodGet, path: vmPath(node, vmid) + "/config"}, &cfg)
	return cfg, err
}

// AssertVMIDFree asks the cluster to confirm that an identifier is unused.
//
// The bare form of this endpoint hands out "the next free id", which knows
// nothing about the range an operator allowed this provider, so the identifier
// is picked here (NextFreeVMID) and only asserted there. A refusal means
// somebody took it between the pick and the clone, which is a conflict to be
// re-picked from, not a failure.
func (c *Client) AssertVMIDFree(ctx context.Context, vmid int) error {
	return c.do(ctx, call{op: fmt.Sprintf("check that VMID %d is free", vmid), ref: strconv.Itoa(vmid),
		verb: http.MethodGet, path: "/cluster/nextid", query: url.Values{"vmid": {strconv.Itoa(vmid)}}}, nil)
}

// CloneRequest is the body of a clone. Storage, Target and Pool are optional:
// left empty, Proxmox uses the template's own.
type CloneRequest struct {
	NewID       int
	Name        string
	Full        bool
	Storage     string
	Target      string
	Pool        string
	Description string
	Format      string
	Snapshot    string
}

func (r CloneRequest) form() url.Values {
	f := url.Values{"newid": {strconv.Itoa(r.NewID)}}
	if r.Full {
		f.Set("full", "1")
	}
	setIf := func(k, v string) {
		if v != "" {
			f.Set(k, v)
		}
	}
	setIf("name", r.Name)
	setIf("storage", r.Storage)
	setIf("target", r.Target)
	setIf("pool", r.Pool)
	setIf("description", r.Description)
	setIf("format", r.Format)
	setIf("snapname", r.Snapshot)
	return f
}

// CloneVM clones a template and returns the task handle to follow.
//
// The handle is the durable half of this call: it is stored against the machine
// row before the call is considered done, so a controller that restarts asks
// what happened rather than cloning again.
func (c *Client) CloneVM(ctx context.Context, node string, template int, req CloneRequest) (string, error) {
	var upid string
	err := c.do(ctx, call{
		op:   fmt.Sprintf("clone template %d to VM %d on %s", template, req.NewID, node),
		ref:  vmRef(node, req.NewID),
		verb: http.MethodPost, path: vmPath(node, template) + "/clone", form: req.form(),
	}, &upid)
	return upid, err
}

// ConfigureVM sets configuration on a guest: its size, its network, and the
// tags and description that mark it as ours. It is synchronous unless it has to
// resize a disk, so the handle it returns is often empty.
func (c *Client) ConfigureVM(ctx context.Context, node string, vmid int, params url.Values) (string, error) {
	var upid string
	err := c.do(ctx, call{
		op:   fmt.Sprintf("configure VM %d on %s", vmid, node),
		ref:  vmRef(node, vmid),
		verb: http.MethodPost, path: vmPath(node, vmid) + "/config", form: params,
	}, &upid)
	return upid, err
}

// ResizeDisk grows a disk, and only ever grows one: Proxmox refuses to shrink,
// and a shape asking for less than the template has is honoured by leaving the
// disk alone rather than by risking a guest's filesystem.
func (c *Client) ResizeDisk(ctx context.Context, node string, vmid int, disk, size string) error {
	return c.do(ctx, call{
		op:   fmt.Sprintf("grow %s on VM %d to %s", disk, vmid, size),
		ref:  vmRef(node, vmid),
		verb: http.MethodPut, path: vmPath(node, vmid) + "/resize",
		form: url.Values{"disk": {disk}, "size": {size}},
	}, nil)
}

// StartVM starts a guest and returns the task handle.
func (c *Client) StartVM(ctx context.Context, node string, vmid int) (string, error) {
	return c.power(ctx, node, vmid, "start", nil)
}

// ShutdownVM asks the guest to shut itself down, waiting up to timeout. It is
// the graceful half: a runner mid-job gets the chance to finish writing.
func (c *Client) ShutdownVM(ctx context.Context, node string, vmid int, timeout time.Duration, forceStop bool) (string, error) {
	form := url.Values{}
	if timeout > 0 {
		form.Set("timeout", strconv.Itoa(int(timeout.Seconds())))
	}
	if forceStop {
		form.Set("forceStop", "1")
	}
	return c.power(ctx, node, vmid, "shutdown", form)
}

// StopVM pulls the power. It is what follows a shutdown that did not finish.
func (c *Client) StopVM(ctx context.Context, node string, vmid int) (string, error) {
	return c.power(ctx, node, vmid, "stop", nil)
}

func (c *Client) power(ctx context.Context, node string, vmid int, action string, form url.Values) (string, error) {
	var upid string
	err := c.do(ctx, call{
		op:   fmt.Sprintf("%s VM %d on %s", action, vmid, node),
		ref:  vmRef(node, vmid),
		verb: http.MethodPost, path: vmPath(node, vmid) + "/status/" + action, form: form,
	}, &upid)
	return upid, err
}

// DeleteVM destroys a guest and returns the task handle.
//
// purge clears the backup and replication references that would otherwise
// outlive the guest, and unreferenced disks go with it: a delete that left
// either behind would keep costing somebody money under a name nothing owns.
func (c *Client) DeleteVM(ctx context.Context, node string, vmid int) (string, error) {
	var upid string
	err := c.do(ctx, call{
		op:   fmt.Sprintf("destroy VM %d on %s", vmid, node),
		ref:  vmRef(node, vmid),
		verb: http.MethodDelete, path: vmPath(node, vmid),
		query: url.Values{"purge": {"1"}, "destroy-unreferenced-disks": {"1"}},
	}, &upid)
	return upid, err
}

// Task reads an asynchronous operation's progress. The handle carries its own
// node, so a controller holding nothing but the stored handle can still ask.
func (c *Client) Task(ctx context.Context, node, upid string) (TaskStatus, error) {
	var t TaskStatus
	err := c.do(ctx, call{
		op:   "read the status of task " + upid,
		ref:  upid,
		verb: http.MethodGet, path: "/nodes/" + url.PathEscape(node) + "/tasks/" + url.PathEscape(upid) + "/status",
	}, &t)
	return t, err
}

// TaskLog reads the last lines a task wrote, which is where the detail behind a
// bare exit status lives and what an operator is shown when a machine failed.
func (c *Client) TaskLog(ctx context.Context, node, upid string, limit int) (string, error) {
	if limit <= 0 {
		limit = 50
	}
	var lines []struct {
		N    Int    `json:"n"`
		Text string `json:"t"`
	}
	err := c.do(ctx, call{
		op:   "read the log of task " + upid,
		ref:  upid,
		verb: http.MethodGet, path: "/nodes/" + url.PathEscape(node) + "/tasks/" + url.PathEscape(upid) + "/log",
		query: url.Values{"limit": {strconv.Itoa(limit)}},
	}, &lines)
	if err != nil {
		return "", err
	}
	texts := make([]string, 0, len(lines))
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	return strings.Join(texts, "\n"), nil
}

// AgentPing asks whether the guest agent is answering yet, which is how a guest
// says it has finished booting far enough to be given its enrolment.
func (c *Client) AgentPing(ctx context.Context, node string, vmid int) error {
	return c.do(ctx, call{
		op:   fmt.Sprintf("ping the guest agent on VM %d", vmid),
		ref:  vmRef(node, vmid),
		verb: http.MethodPost, path: vmPath(node, vmid) + "/agent/ping",
	}, nil)
}

// AgentFileWrite writes a file inside the guest.
//
// Proxmox has no parameter for the file's mode, so a caller that needs one
// writes the file and then runs a command to fix it -- which is the whole
// reason provider.File carries a mode at all.
func (c *Client) AgentFileWrite(ctx context.Context, node string, vmid int, path string, content []byte) error {
	return c.do(ctx, call{
		op:   fmt.Sprintf("write %s inside VM %d", path, vmid),
		ref:  vmRef(node, vmid),
		verb: http.MethodPost, path: vmPath(node, vmid) + "/agent/file-write",
		form: url.Values{"file": {path}, "content": {string(content)}},
	}, nil)
}

// AgentExec starts a command inside the guest and returns its process id.
//
// The command is an argv array rather than a shell line, and deliberately so: a
// payload that is never pasted into a shell has no quoting to get wrong and no
// injection class to police.
func (c *Client) AgentExec(ctx context.Context, node string, vmid int, argv []string, input string) (int, error) {
	if len(argv) == 0 {
		return 0, &provider.Error{Kind: provider.FailureInternal, Op: "run a command inside the guest",
			Message: "no command was given"}
	}
	form := url.Values{"command": argv}
	if input != "" {
		form.Set("input-data", input)
	}
	var out struct {
		PID Int `json:"pid"`
	}
	err := c.do(ctx, call{
		op:   fmt.Sprintf("run %q inside VM %d", strings.Join(argv, " "), vmid),
		ref:  vmRef(node, vmid),
		verb: http.MethodPost, path: vmPath(node, vmid) + "/agent/exec", form: form,
	}, &out)
	return out.PID.Int(), err
}

// AgentExecStatus reads back what a command inside the guest did.
func (c *Client) AgentExecStatus(ctx context.Context, node string, vmid, pid int) (AgentExecStatus, error) {
	var s AgentExecStatus
	err := c.do(ctx, call{
		op:   fmt.Sprintf("read the result of process %d inside VM %d", pid, vmid),
		ref:  vmRef(node, vmid),
		verb: http.MethodGet, path: vmPath(node, vmid) + "/agent/exec-status",
		query: url.Values{"pid": {strconv.Itoa(pid)}},
	}, &s)
	return s, err
}

// NextFreeVMID is the lowest identifier in [lo, hi] that nothing already uses.
//
// Picking it here rather than trusting the cluster's own "next free id" is what
// makes an explicitly configured range mean anything: that endpoint knows about
// the whole cluster and nothing about the range an operator allowed this
// provider, so a fleet driven by it would spread guests across identifiers
// somebody else's automation is entitled to. Taken may hold identifiers this
// fleet has allocated but not yet created, which is the race that makes two
// machines out of one create.
func NextFreeVMID(taken []int, lo, hi int) (int, error) {
	if lo <= 0 || hi <= 0 || lo > hi {
		return 0, &provider.Error{
			Kind: provider.FailureConfig, Op: "pick a VMID",
			Message: fmt.Sprintf("the allowed VMID range %d-%d is not a range", lo, hi),
			Remedy:  "set vmid_min below vmid_max, both above zero; Proxmox itself allows 100 upwards",
		}
	}
	used := make(map[int]bool, len(taken))
	for _, id := range taken {
		used[id] = true
	}
	for id := lo; id <= hi; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, &provider.Error{
		Kind: provider.FailureQuota, Op: "pick a VMID",
		Message: fmt.Sprintf("every identifier between %d and %d is in use", lo, hi),
		Remedy:  "widen the vmid_min..vmid_max range, or remove guests that no longer need their identifiers",
	}
}

func vmPath(node string, vmid int) string {
	return "/nodes/" + url.PathEscape(node) + "/qemu/" + strconv.Itoa(vmid)
}

// vmRef is how a guest is named in a failure: the node and the identifier, the
// two things an operator needs to find it in the console.
func vmRef(node string, vmid int) string { return fmt.Sprintf("%s/qemu/%d", node, vmid) }
