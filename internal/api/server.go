package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/cryptox"
)

// Options are what the API needs to serve. Everything else it uses -- the
// store, the auth service, the event bus, the configuration -- it takes from
// the controller, so there is exactly one wiring of those and no way for the
// API to end up talking to a different database than the fleet does.
type Options struct {
	// Controller is the running control plane. Required.
	Controller *controller.Controller
	// Logger is the process logger; nil uses slog's default.
	Logger *slog.Logger
}

// Server is the HTTP surface. Build it with New and either hand Handler() to a
// listener of your own or let ListenAndServe run one.
type Server struct {
	ctrl *controller.Controller
	cfg  *config.Config
	auth *auth.Service
	log  *slog.Logger

	// key seals the GitHub App private keys and webhook secrets that arrive on
	// the installations endpoints. It is loaded from the same configuration the
	// controller was built with rather than passed in, so the two can never be
	// sealing with different keys; keyErr explains a failure to the one handler
	// that needs it instead of stopping the whole server from starting.
	key    *cryptox.Key
	keyErr error

	// oidc is nil when single sign-on is off or its discovery failed at
	// startup; oidcErr then carries the reason, which the SSO routes report
	// rather than pretending the button was never there.
	oidc    *auth.OIDCProvider
	oidcErr error

	spa *spaHandler
	csp string
	// formTargets are the GitHub origins the page policy lets the App
	// manifest form post to; handleCreateManifest refuses to build a manifest
	// for any other, since the browser would refuse to send it.
	formTargets []string
	trusted     []*net.IPNet
	manifests   *manifestStates

	// settingsMu serialises PATCH /settings against itself. The configuration
	// it writes into is the one the controller's loops read, so two operators
	// changing different keys at the same moment must not interleave.
	settingsMu sync.Mutex

	handler http.Handler
}

// New builds the server. It does no I/O beyond loading the encryption key and,
// when single sign-on is configured, discovering the identity provider.
func New(opts Options) (*Server, error) {
	if opts.Controller == nil {
		return nil, errors.New("api: no controller; build one with controller.New before the API that serves it")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "api")

	cfg := opts.Controller.Config()
	s := &Server{
		ctrl:      opts.Controller,
		cfg:       cfg,
		auth:      opts.Controller.Auth(),
		log:       log,
		manifests: newManifestStates(opts.Controller.Now),
	}

	spa, err := newSPAHandler(cfg.Server.ExternalURL)
	if err != nil {
		return nil, err
	}
	s.spa = spa
	s.formTargets = manifestFormTargets(cfg.GitHub.APIBaseURL)
	s.csp = contentSecurityPolicy(spa.inlineScriptHashes(), s.formTargets)

	s.key, s.keyErr = loadKey(cfg)
	if s.keyErr != nil {
		// Not fatal: a controller with no installations yet is perfectly
		// usable, and refusing to serve the UI would hide the message that
		// says how to fix this.
		log.Warn("no usable encryption key, so GitHub App credentials cannot be accepted or read",
			"error", s.keyErr, "fix", "set security.encryption_key_file, or ZOOMIES_ENCRYPTION_KEY")
	}

	s.trusted, err = parseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		return nil, err
	}

	if err := checkMountPaths(cfg); err != nil {
		return nil, err
	}

	if cfg.Security.DisableAuth {
		// Once, at startup, rather than per request: a warning printed on every
		// call is a warning nobody reads, and this one matters.
		log.Warn("authentication is disabled; every request is treated as an administrator",
			"setting", "security.disable_auth",
			"fix", "remove security.disable_auth (or ZOOMIES_DISABLE_AUTH) before this instance is reachable by anyone else")
	}

	s.initOIDC()
	s.handler = s.routes()
	return s, nil
}

// Handler returns the whole surface: API, webhook, metrics and UI. It is safe
// to serve from a listener of the caller's own, which is what the tests and the
// installer's health check do.
func (s *Server) Handler() http.Handler { return s.handler }

// initOIDC discovers the identity provider, if there is one.
//
// A provider that is down when the controller boots must not stop the
// controller booting: password login still works, and the login page needs to
// come up to say so. The error is kept and returned by the SSO routes.
func (s *Server) initOIDC() {
	if !s.cfg.OIDC.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := auth.NewOIDC(ctx, s.cfg.OIDC, s.cfg.Server.ExternalURL)
	if err != nil {
		s.oidcErr = err
		s.log.Error("single sign-on is configured but could not be set up; password login still works",
			"issuer", s.cfg.OIDC.Issuer, "error", err)
		return
	}
	s.oidc = p
}

// loadKey resolves the instance encryption key from the same configuration the
// controller was built with.
func loadKey(cfg *config.Config) (*cryptox.Key, error) {
	if raw := strings.TrimSpace(cfg.Security.EncryptionKey); raw != "" {
		return cryptox.ParseKey(raw)
	}
	if path := strings.TrimSpace(cfg.Security.EncryptionKeyFile); path != "" {
		return cryptox.LoadKeyFile(path)
	}
	return nil, cryptox.ErrNoKey
}

// parseTrustedProxies turns the configured CIDRs into networks, accepting a
// bare address as a single host so an operator does not have to write /32.
// The cloudflare token expands to Cloudflare's published ranges first, so
// what is believed is exactly what the operator named.
func parseTrustedProxies(in []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, raw := range config.ExpandTrustedProxies(in) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(raw); err == nil {
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, fmt.Errorf("api: server.trusted_proxies: %q is not an IP address or CIDR block; write something like 10.0.0.0/8 or 192.0.2.7", raw)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out, nil
}

// checkMountPaths refuses a configuration whose webhook or metrics path would
// collide with something else this server serves.
//
// The router would otherwise panic on a duplicate pattern, or -- worse -- the
// webhook would quietly shadow part of the API. Both are configuration
// mistakes, so they are reported as such, naming the setting to change.
func checkMountPaths(cfg *config.Config) error {
	reserved := map[string]string{
		"/":                 "the UI",
		"/healthz":          "the liveness probe",
		"/readyz":           "the readiness probe",
		"/api/openapi.yaml": "the API specification",
	}
	check := func(setting, path string) error {
		if what, taken := reserved[path]; taken {
			return fmt.Errorf("api: %s is %q, which is already %s; choose another path", setting, path, what)
		}
		if strings.HasPrefix(path, "/api/") {
			return fmt.Errorf("api: %s is %q; /api/ is the machine API's own namespace, so choose a path outside it", setting, path)
		}
		reserved[path] = setting
		return nil
	}
	if err := check("github.webhook_path", cfg.GitHub.WebhookPath); err != nil {
		return err
	}
	if cfg.Metrics.Enabled {
		if err := check("metrics.path", cfg.Metrics.Path); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Listening
// ---------------------------------------------------------------------------

// shutdownGrace is how long an in-flight request has to finish once the
// listener stops accepting. Streams are cancelled first, so this covers
// ordinary requests only.
const shutdownGrace = 15 * time.Second

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
//
// It returns nil on a clean shutdown; anything else is a failure worth exiting
// on. The listener is opened before the readiness notification is sent, so a
// service manager that starts a dependent unit on READY=1 is not racing the
// bind.
func (s *Server) ListenAndServe(ctx context.Context) error {
	// A cancellable base context is what makes shutdown tidy. An SSE stream or
	// a log tail is an ordinary request as far as net/http is concerned, and
	// Shutdown waits for requests rather than cancelling them; cancelling this
	// context first is what tells those handlers to return.
	baseCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	srv := s.httpServer(baseCtx)

	tlsCfg, err := s.tlsConfig()
	if err != nil {
		return err
	}
	srv.TLSConfig = tlsCfg

	ln, err := net.Listen("tcp", s.cfg.Server.Bind)
	if err != nil {
		return fmt.Errorf("api: cannot listen on %s: %w (another process may already be using that address; change server.bind)", s.cfg.Server.Bind, err)
	}
	scheme := "http"
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
		scheme = "https"
	}

	s.log.Info("serving", "address", ln.Addr().String(), "scheme", scheme,
		"external_url", s.cfg.Server.ExternalURL, "ui", s.spa.built)
	notifyReady()

	errs := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	s.log.Info("shutting down the listener")
	// Streams first, then the graceful wait: an SSE client holding a
	// connection open would otherwise keep Shutdown blocked for its full grace
	// period every single time.
	cancelRequests()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Warn("some connections did not close in time; closing them", "error", err)
		_ = srv.Close()
	}
	<-errs
	return nil
}

// httpServer builds the listener's server.
//
// It is separate from ListenAndServe so that the timeouts can be asserted in a
// test: WriteTimeout in particular has to stay zero, and "somebody set it to 30
// seconds because every other timeout is set" is exactly the change that would
// otherwise ship and quietly cut every event stream off after half a minute.
func (s *Server) httpServer(baseCtx context.Context) *http.Server {
	return &http.Server{
		Handler: s.handler,
		// WriteTimeout MUST stay 0. It is a deadline on the whole response, so
		// any non-zero value cuts off the event stream and every followed log
		// tail exactly that long after they start -- which looks to an operator
		// like the fleet going quiet. Slow clients are bounded by IdleTimeout
		// and by ReadHeaderTimeout instead.
		WriteTimeout: 0,
		// ReadTimeout covers the request body too, which would cut off the
		// agent's chunked log relay; the handlers that stream a body clear
		// their own read deadline with an http.ResponseController.
		ReadTimeout:       s.cfg.Server.ReadTimeout,
		ReadHeaderTimeout: readHeaderTimeout(s.cfg.Server.ReadTimeout),
		IdleTimeout:       s.cfg.Server.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
}

// readHeaderTimeout keeps a slow-loris client from holding a connection open
// with a partial request even when server.read_timeout has been raised.
func readHeaderTimeout(readTimeout time.Duration) time.Duration {
	const cap = 20 * time.Second
	if readTimeout > 0 && readTimeout < cap {
		return readTimeout
	}
	return cap
}

// tlsConfig builds the listener's TLS configuration, or nil for plain HTTP.
func (s *Server) tlsConfig() (*tls.Config, error) {
	t := s.cfg.Server.TLS
	switch t.Mode {
	case "", config.TLSOff:
		return nil, nil

	case config.TLSFiles:
		if t.CertFile == "" || t.KeyFile == "" {
			return nil, errors.New("api: server.tls.mode is \"files\" but server.tls.cert_file and server.tls.key_file are not both set; give both, or use mode \"self-signed\"")
		}
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("api: loading the certificate from %s and %s: %w", t.CertFile, t.KeyFile, err)
		}
		return baseTLS(cert), nil

	case config.TLSSelfSigned:
		cert, err := s.selfSignedCertificate()
		if err != nil {
			return nil, err
		}
		return baseTLS(cert), nil

	default:
		return nil, fmt.Errorf("api: server.tls.mode %q is not one of off, self-signed or files", t.Mode)
	}
}

func baseTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}
}

// selfSignedPaths returns where a generated certificate lives. The state
// directory is used rather than the config directory because the certificate is
// generated data, and the agent's ca_file error message points operators at
// exactly this path.
func (s *Server) selfSignedPaths() (certPath, keyPath string) {
	certPath, keyPath = s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile
	if certPath == "" {
		certPath = filepath.Join(config.StateDir(), "tls", "cert.pem")
	}
	if keyPath == "" {
		keyPath = filepath.Join(config.StateDir(), "tls", "key.pem")
	}
	return certPath, keyPath
}

// selfSignedCertificate loads the generated certificate, making one the first
// time and whenever the stored one has expired.
//
// It is persisted rather than regenerated per start because an agent that has
// pinned it with agent.ca_file, and an operator who has clicked through the
// browser warning, would both have to be redone on every restart otherwise.
func (s *Server) selfSignedCertificate() (tls.Certificate, error) {
	certPath, keyPath := s.selfSignedPaths()

	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		leaf, perr := x509.ParseCertificate(cert.Certificate[0])
		if perr == nil && time.Now().Before(leaf.NotAfter.Add(-24*time.Hour)) {
			return cert, nil
		}
		s.log.Info("the stored self-signed certificate has expired; generating a new one", "cert", certPath)
	} else if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		s.log.Warn("could not read the stored self-signed certificate; generating a new one",
			"cert", certPath, "error", err)
	}

	certPEM, keyPEM, err := generateSelfSigned(s.certificateHosts(), time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o750); err != nil {
		return tls.Certificate{}, fmt.Errorf("api: creating %s for the generated certificate: %w", filepath.Dir(certPath), err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, fmt.Errorf("api: writing %s: %w", certPath, err)
	}
	// The private key is readable only by the service user; the certificate is
	// world-readable on purpose, because agents copy it to pin with.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("api: writing %s: %w", keyPath, err)
	}
	s.log.Info("generated a self-signed certificate; GitHub will not deliver webhooks to it, so scaling will use the fallback poller",
		"cert", certPath, "hosts", strings.Join(s.certificateHosts(), ", "),
		"fix", "pin it on each agent with agent.ca_file, or use a certificate GitHub trusts with server.tls.mode: files")

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("api: the generated certificate could not be loaded back: %w", err)
	}
	return cert, nil
}

// certificateHosts is every name a generated certificate should answer to: what
// the operator configured, the external URL's host, the machine's own name, and
// loopback -- because the installer's own health check dials 127.0.0.1.
func (s *Server) certificateHosts() []string {
	hosts := slices.Clone(s.cfg.Server.TLS.Hosts)
	if u, err := url.Parse(s.cfg.Server.ExternalURL); err == nil && u.Hostname() != "" {
		hosts = append(hosts, u.Hostname())
	}
	if h, _, err := net.SplitHostPort(s.cfg.Server.Bind); err == nil && h != "" && h != "0.0.0.0" && h != "::" {
		hosts = append(hosts, h)
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		hosts = append(hosts, name)
	}
	hosts = append(hosts, "localhost", "127.0.0.1", "::1")

	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// selfSignedValidity is a little over two years: long enough not to be a
// recurring chore, short enough that a pinned certificate is rotated within the
// life of a deployment.
const selfSignedValidity = 825 * 24 * time.Hour

// generateSelfSigned mints a certificate for hosts, returning PEM blocks.
func generateSelfSigned(hosts []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("api: generating a key for the self-signed certificate: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("api: generating a certificate serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: firstOr(hosts, "zoomies"), Organization: []string{"Zoomies"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Self-signed and self-issued: agents pin this certificate directly
		// with agent.ca_file, which needs it to be a valid CA for itself.
		IsCA: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("api: creating the self-signed certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("api: encoding the certificate's private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func firstOr(in []string, fallback string) string {
	if len(in) > 0 && in[0] != "" {
		return in[0]
	}
	return fallback
}

// notifyReady sends systemd's READY=1 when the process was started by systemd.
//
// It is done by hand rather than with a dependency: the protocol is one datagram
// to the socket named in NOTIFY_SOCKET, and a service manager that is not
// systemd simply does not set the variable, so this is a no-op everywhere else.
func notifyReady() {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return
	}
	// An abstract socket is spelled with a leading '@' in the environment and a
	// NUL in the address.
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		slog.Default().Debug("could not notify the service manager that we are ready", "socket", socket, "error", err)
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("READY=1"))
}
