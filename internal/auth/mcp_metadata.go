package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

// Client ID Metadata Documents.
//
// A client that has no prior relationship with this controller -- the usual
// case for an MCP client -- can name itself by an https URL and publish its
// name and redirect URIs there, rather than registering. That means this
// process makes an outbound request on the say-so of an unauthenticated one,
// which is the shape of a server-side request forgery, so the fetch is fenced:
// https only, no redirects, a public address unless the operator allowed
// private egress, a short timeout and a small body.

const (
	metadataMaxBytes = 5 << 10
	metadataTimeout  = 5 * time.Second
	// metadataFresh is how long a fetched document is trusted before the next
	// sign-in fetches it again.
	metadataFresh = time.Hour
)

// IsMetadataClientID reports whether a client_id is a metadata document URL:
// https, with a host and a path, and nothing that would make the fetched
// document a different one from the one named.
func IsMetadataClientID(id string) bool {
	u, err := url.Parse(id)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Path != "" && u.Path != "/" &&
		u.Fragment == "" && u.User == nil && !strings.Contains(u.Path, "/./") && !strings.Contains(u.Path, "/../")
}

type metadataDoc struct {
	name      string
	uri       string
	redirects []string
}

// metadataFetcher fetches and checks client metadata documents, and remembers
// which ones it fetched recently.
type metadataFetcher struct {
	// get fetches a document. Tests replace it; the default is the fenced
	// HTTP client below.
	get func(ctx context.Context, url string, allowPrivate bool) ([]byte, error)

	mu      sync.Mutex
	fetched map[string]time.Time
}

func newMetadataFetcher() *metadataFetcher {
	return &metadataFetcher{get: fetchPublic, fetched: map[string]time.Time{}}
}

func (m *metadataFetcher) fresh(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.fetched[id]
	return ok && now.Sub(at) < metadataFresh
}

func (m *metadataFetcher) remember(id string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.fetched) > 1000 {
		m.fetched = map[string]time.Time{}
	}
	m.fetched[id] = now
}

func (m *metadataFetcher) fetch(ctx context.Context, id string, allowPrivate bool) (*metadataDoc, error) {
	body, err := m.get(ctx, id, allowPrivate)
	if err != nil {
		return nil, err
	}
	var doc struct {
		ClientID                string   `json:"client_id"`
		ClientName              string   `json:"client_name"`
		ClientURI               string   `json:"client_uri"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("it is not a JSON object: %w", err)
	}
	// The document has to name itself, or anybody could host a copy of a
	// well-known client's document and borrow its name on the consent screen.
	if doc.ClientID != id {
		return nil, fmt.Errorf("its client_id is %q, not the URL it was fetched from", doc.ClientID)
	}
	switch doc.TokenEndpointAuthMethod {
	case "", "none":
	default:
		return nil, fmt.Errorf("it asks for token_endpoint_auth_method %q, and this controller accepts a metadata client only as a public client", doc.TokenEndpointAuthMethod)
	}
	redirects, err := checkRedirectURIs(doc.RedirectURIs)
	if err != nil {
		var oe *OAuthError
		if errors.As(err, &oe) {
			return nil, errors.New(oe.Description)
		}
		return nil, err
	}
	return &metadataDoc{name: clientName(doc.ClientName, doc.ClientURI, redirects), uri: safeHTTPS(doc.ClientURI), redirects: redirects}, nil
}

// fetchPublic is the fenced fetch.
func fetchPublic(ctx context.Context, raw string, allowPrivate bool) ([]byte, error) {
	dialer := &net.Dialer{
		Timeout: metadataTimeout,
		// The check runs on the address actually dialled, after resolution,
		// so a name that resolves to 169.254.169.254 is refused however it is
		// spelled -- and a name re-pointed between lookup and connect too.
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			return dialTarget(host)
		},
	}
	client := &http.Client{
		Timeout: metadataTimeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: metadataTimeout,
			// No proxy: the address check above is on the address dialled,
			// and through a proxy that would be the proxy's.
			Proxy: nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("it redirected, and a client ID has to be the document's own address")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("it answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, metadataMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > metadataMaxBytes {
		return nil, fmt.Errorf("it is larger than %d bytes", metadataMaxBytes)
	}
	return body, nil
}

// dialTarget refuses an address a metadata fetch is about to connect to when
// it is not public. The ranges are config's, the ones every other outbound
// URL is judged by: a list kept here drifted short of them once, missing the
// rest of 0.0.0.0/8, the reserved and benchmarking ranges, and NAT64 and 6to4
// addresses that carry a private IPv4 destination inside them.
func dialTarget(host string) error {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%s is not a public address", host)
	}
	if what := config.PrivateAddress(addr); what != "" {
		return fmt.Errorf("%s is not a public address: it is %s", host, what)
	}
	return nil
}

// WithClientMetadataFetcher replaces how metadata documents are fetched.
// Tests use it to serve a document without a network.
func WithClientMetadataFetcher(fn func(ctx context.Context, url string) ([]byte, error)) Option {
	return func(s *Service) {
		s.metadata.get = func(ctx context.Context, u string, _ bool) ([]byte, error) { return fn(ctx, u) }
	}
}
