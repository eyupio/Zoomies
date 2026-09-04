package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/logging"
)

// SessionCookie is the name of the cookie a browser session lives in. It is
// exported because the OpenAPI document names it and the CLI's browser-login
// helper has to read it back.
const SessionCookie = "zoomies_session"

// ctxKey is this package's private context key type, so nothing outside can
// collide with -- or forge -- what the middleware attaches.
type ctxKey int

const (
	ctxRequestInfo ctxKey = iota
	// ctxAgentHost carries the host an agent request authenticated as.
	ctxAgentHost
)

// requestInfo is what the middleware chain learns about a request, in one
// place.
//
// It is a pointer in the context and filled in as the chain goes, because the
// outermost middleware (which logs the request) needs the identity that an
// inner one resolves, and a middleware cannot see a context its own callee
// replaced.
type requestInfo struct {
	id string
	ip string
	// identity is nil until the authentication middleware has run, and stays
	// nil on the unauthenticated routes.
	identity *auth.Identity
	log      *slog.Logger
}

func infoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(ctxRequestInfo).(*requestInfo)
	return info
}

// Identity returns the authenticated caller, or nil on an unauthenticated
// route. Handlers use it for audit attribution and for the checks that depend
// on who is asking rather than on the route alone.
func Identity(ctx context.Context) *auth.Identity {
	if info := infoFrom(ctx); info != nil {
		return info.identity
	}
	return nil
}

// clientIP returns the address a request came from, resolved through the proxies
// the operator has said to trust.
func ClientIP(ctx context.Context) string {
	if info := infoFrom(ctx); info != nil {
		return info.ip
	}
	return ""
}

// RequestID returns the identifier this request is logged under, which is what
// an internal error hands back so an operator can find the log line.
func RequestID(ctx context.Context) string {
	if info := infoFrom(ctx); info != nil {
		return info.id
	}
	return ""
}

// logger returns the request-scoped logger.
func (s *Server) logger(r *http.Request) *slog.Logger {
	if info := infoFrom(r.Context()); info != nil && info.log != nil {
		return info.log
	}
	return s.log
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// authenticate resolves the caller and refuses the request when it cannot.
//
// When security.disable_auth is set, auth.Authenticate hands back the
// "auth-disabled" administrator for every request; the warning about that is
// logged once at startup rather than here, because a line per request is a line
// nobody reads.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := infoFrom(r.Context())
		id, err := s.resolveIdentity(r)
		if err != nil {
			unauthorized(w, err.Error())
			return
		}
		if info != nil {
			info.identity = id
			info.log = info.log.With("identity", id.String())
		}
		next.ServeHTTP(w, r)
	})
}

// optionalAuth resolves the caller when a credential is present but lets the
// request through when it is not. It is what /meta and the login routes use, so
// that a signed-in operator's actions are still attributed in the audit log.
func (s *Server) optionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, err := s.resolveIdentity(r); err == nil {
			if info := infoFrom(r.Context()); info != nil {
				info.identity = id
			}
		}
		next.ServeHTTP(w, r)
	})
}

// resolveIdentity turns the request's credentials into an identity.
func (s *Server) resolveIdentity(r *http.Request) (*auth.Identity, error) {
	in := auth.AuthInput{
		Authorization: r.Header.Get("Authorization"),
		IP:            ClientIP(r.Context()),
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		in.SessionCookie = c.Value
	}
	return s.auth.Authenticate(r.Context(), in)
}

func hasBearer(r *http.Request) bool {
	scheme, _, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	return ok && strings.EqualFold(scheme, "bearer")
}

// require refuses a caller whose role or scopes do not reach the action.
//
// The policy lives in internal/auth's table, not here: this middleware only
// asks. That is what makes "every route has a role" a property a test can walk,
// and what stops a new endpoint from shipping without anyone deciding who may
// call it.
func (s *Server) require(action auth.Action) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := Identity(r.Context())
			if id == nil {
				unauthorized(w, auth.ErrNoCredentials.Error())
				return
			}
			if !auth.Allowed(id, action) {
				forbidden(w, auth.Explain(id, action))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// Cross-site request forgery
// ---------------------------------------------------------------------------

// csrf refuses an unsafe cross-origin request that carries the session cookie.
//
// Bearer requests are exempt, and deliberately so: a browser cannot attach an
// Authorization header to a cross-origin request without a CORS preflight,
// which this server never grants, so a token-authenticated call cannot be made
// by a page the operator merely visited. A cookie, by contrast, rides along on
// its own, which is exactly what makes the check necessary here.
//
// The check runs on the whole /api/v1 subtree rather than only on
// cookie-authenticated routes, because POST /auth/login mints the cookie and a
// login CSRF -- signing the victim into the attacker's account -- is worth
// refusing too.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if hasBearer(r) {
			next.ServeHTTP(w, r)
			return
		}
		if msg := s.checkOrigin(r); msg != "" {
			forbidden(w, msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkOrigin returns "" when the request is same-origin (or from an origin the
// operator allowed), and otherwise the sentence to refuse it with.
func (s *Server) checkOrigin(r *http.Request) string {
	// Sec-Fetch-Site is the browser's own answer and cannot be set by script.
	// "none" is a user-initiated navigation, which is not a forged request.
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "same-origin", "none":
		return ""
	case "":
		// Not sent: either an older browser or a non-browser client. Fall
		// through to the Origin header.
	default:
		if s.originAllowed(r.Header.Get("Origin")) || sameOriginAsRequest(r, r.Header.Get("Origin")) {
			return ""
		}
		from := strings.TrimSpace(r.Header.Get("Origin"))
		if from == "" {
			from = "another site"
		}
		return "this request came from " + from + " (Sec-Fetch-Site: " + r.Header.Get("Sec-Fetch-Site") +
			"), which is not this controller's origin. Add it to server.allowed_origins if that is deliberate, or use an API token."
	}

	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if r.Header.Get("Referer") == "" && !hasCookie(r, SessionCookie) {
			// No cookie, no browser headers: a plain client that is about to be
			// asked for credentials anyway.
			return ""
		}
		return "this request carried no Origin or Sec-Fetch-Site header, so it cannot be shown to have come from the Zoomies UI. " +
			"Use a browser, or authenticate with an API token, which is exempt from this check."
	}
	if s.originAllowed(origin) || sameOriginAsRequest(r, origin) {
		return ""
	}
	return "this request came from " + origin + ", which is not this controller's origin. " +
		"Add it to server.allowed_origins if that is deliberate, or use an API token."
}

// originAllowed reports whether an Origin header names this controller or one
// of the origins the operator listed.
func (s *Server) originAllowed(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return false
	}
	for _, allowed := range s.cfg.Server.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(strings.TrimSpace(allowed), "/"), strings.TrimRight(origin, "/")) {
			return true
		}
		if allowed == "*" {
			return true
		}
	}
	if ext := s.cfg.Server.ExternalURL; ext != "" {
		if u, err := url.Parse(ext); err == nil && sameOrigin(u, origin) {
			return true
		}
	}
	return false
}

// sameOrigin compares an Origin header with a URL's scheme and host.
func sameOrigin(u *url.URL, origin string) bool {
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(o.Scheme, u.Scheme) && strings.EqualFold(o.Host, u.Host)
}

// sameOriginAsRequest reports whether origin matches the host the request was
// addressed to. It is the fallback when server.external_url is not configured,
// which is the common case on a loopback development instance.
func sameOriginAsRequest(r *http.Request, origin string) bool {
	o, err := url.Parse(origin)
	if err != nil || o.Host == "" {
		return false
	}
	if !strings.EqualFold(o.Host, r.Host) {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.ToLower(proto)
	}
	return strings.EqualFold(o.Scheme, scheme)
}

func hasCookie(r *http.Request, name string) bool {
	_, err := r.Cookie(name)
	return err == nil
}

// ---------------------------------------------------------------------------
// The session cookie
// ---------------------------------------------------------------------------

// setSessionCookie installs a browser session.
//
// SameSite=Lax rather than Strict so that following a link into Zoomies from
// another tab does not land on a login page; the same-origin check above is
// what defends the unsafe methods, and it does not depend on the cookie policy.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecureValue(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.auth.SessionTTL() / time.Second),
	})
}

// clearSessionCookie expires the cookie. The attributes have to match the ones
// it was set with or the browser keeps the old cookie alongside the new one.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecureValue(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

// ---------------------------------------------------------------------------
// Agent authentication
// ---------------------------------------------------------------------------

// agentAuth resolves an agent's own token to its host.
//
// Agent credentials are a separate class from user ones: they are accepted on
// /api/v1/agent/* and nowhere else, and an identity from here carries the
// viewer role purely so that audit rows have an actor. The host is what the
// handlers actually work with, which is why it goes into the context too.
func (s *Server) agentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, err := s.auth.AuthenticateAgent(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrNoCredentials):
				unauthorized(w, "this endpoint is for agents; send the agent token issued at join as a bearer token")
			case errors.Is(err, auth.ErrInvalidCredentials):
				unauthorized(w, "this controller does not recognise that agent token; re-join the host with a fresh join token (zoomies agent join <controller-url> --token <join-token>)")
			default:
				s.internal(w, r, "authenticating an agent", err)
			}
			return
		}
		info := infoFrom(r.Context())
		id := auth.AgentIdentity(h, ClientIP(r.Context()))
		if info != nil {
			info.identity = id
			info.log = info.log.With("host", h.ID, "host_name", h.Name)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxAgentHost, h)))
	})
}

// ---------------------------------------------------------------------------
// Client addresses
// ---------------------------------------------------------------------------

// resolveClientIP is the real-IP resolver.
//
// X-Forwarded-For is believed only when the connection itself came from a CIDR
// in server.trusted_proxies, and then only the right-most entry that is not
// itself a trusted proxy is taken. Anything else lets a client pick its own
// address, and with it defeat the login rate limiter and forge the address in
// every audit row it causes.
func (s *Server) resolveClientIP(r *http.Request) string {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remote = r.RemoteAddr
	}
	if len(s.trusted) == 0 || !s.isTrustedProxy(remote) {
		return remote
	}

	forwarded := r.Header.Values("X-Forwarded-For")
	var chain []string
	for _, header := range forwarded {
		for _, part := range strings.Split(header, ",") {
			if part = strings.TrimSpace(part); part != "" {
				chain = append(chain, part)
			}
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if ip := net.ParseIP(strings.Trim(chain[i], "[]")); ip != nil && !s.isTrustedProxy(chain[i]) {
			return ip.String()
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		if ip := net.ParseIP(real); ip != nil {
			return ip.String()
		}
	}
	return remote
}

func (s *Server) isTrustedProxy(addr string) bool {
	ip := net.ParseIP(strings.Trim(addr, "[]"))
	if ip == nil {
		return false
	}
	return slices.ContainsFunc(s.trusted, func(n *net.IPNet) bool { return n.Contains(ip) })
}

// withRequestLogger attaches the request-scoped logger to the context so that
// anything downstream -- including the controller, which reads it through
// logging.FromContext -- logs with the request ID attached.
func withRequestLogger(ctx context.Context, l *slog.Logger) context.Context {
	return logging.WithLogger(ctx, l)
}
