package api

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

const tailcatIdentitySetting = "tailcat.identity.v1"

// tailcatCheckInterval is how often a wanted listener checks that its relay
// still answers. It is well inside the minute an operator should wait to be
// told the private hosts are cut off, and slow enough that a relay which is
// down is asked three times a minute rather than hammered.
const tailcatCheckInterval = 20 * time.Second

// tailcatHealthyInterval is how often a listener that is healthy on its sealed
// relay checks it. The public relays are documented as rate-limited, so a
// probe every twenty seconds from every controller is traffic nobody should
// generate while nothing is wrong; five minutes still notices an outage well
// before a host's heartbeats have been missed for long, and the moment it is
// noticed the loop drops back to tailcatCheckInterval.
const tailcatHealthyInterval = 5 * time.Minute

// relayProbeTimeout bounds one relay probe. A relay that takes longer than
// this to answer an HTTPS request is not one an agent can hold a session on.
const relayProbeTimeout = 5 * time.Second

type tailcatContextKey struct{}

// The listener exists only in memory. It cannot expose a port on the OS, route
// the home LAN, or give a tunnel peer access to the operator API.
type tunnelListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *tunnelListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case c := <-l.connections:
		return c, nil
	}
}
func (l *tunnelListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *tunnelListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80} }
func (l *tunnelListener) offer(c net.Conn) {
	select {
	case <-l.done:
		_ = c.Close()
	case l.connections <- c:
	}
}

type privateConnections struct {
	mu       sync.Mutex
	closed   bool
	node     *tailcat.Server
	http     *http.Server
	listener *tunnelListener
	address  string

	// wanted is set once an identity exists, so the check loop knows a
	// listener that is not running is one that failed rather than one nobody
	// has asked for yet -- the latter must never dial a relay.
	wanted bool
	// sealed is the relay region the identity was sealed with, and region the
	// one the running node uses. They differ only while the sealed relay is
	// unreachable and the expansion found another that answers; the sealed one
	// stays the first choice, because it is the one every enrolled host's
	// address names.
	sealed *tailcfg.DERPRegion
	region *tailcfg.DERPRegion

	// expand and probe are the two network calls the listener makes on its
	// own behalf, replaceable so a test can hold a relay down without the
	// public relay map.
	expand func(context.Context) (*tailcfg.DERPRegion, error)
	probe  func(context.Context, *tailcfg.DERPRegion) error
}

// Only transport-observed facts mark hosts as Tailcat hosts. A JSON field or
// proxy header from an agent cannot grant it that badge.
func connectionKind(ctx context.Context) string {
	if ctx.Value(tailcatContextKey{}) == true {
		return "tailcat"
	}
	return "direct"
}

func (s *Server) privateAgentHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := r.URL.Path == agent.PathJoin || r.URL.Path == agent.PathHeartbeat ||
			r.URL.Path == agent.PathTasks || r.URL.Path == agent.PathResults || r.URL.Path == agent.PathReport ||
			strings.HasPrefix(r.URL.Path, agent.PathLogs+"/") ||
			(r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/provider-setups/") && strings.HasSuffix(r.URL.Path, "/complete"))
		if !allowed {
			http.NotFound(w, r)
			return
		}
		for _, key := range []string{"Forwarded", "X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP", "X-Forwarded-Proto"} {
			r.Header.Del(key)
		}
		s.handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tailcatContextKey{}, true)))
	})
}

// expandRelay picks a relay region afresh from the public relay map, bounded
// by a context because the upstream library would otherwise do the same
// discovery with a background one inside Start.
func expandRelay(ctx context.Context) (*tailcfg.DERPRegion, error) {
	ci := &tailcat.ConnInfo{RegionID: -1}
	resolveCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := ci.Expand(resolveCtx, tailcat.ExpandForServer); err != nil {
		return nil, err
	}
	if len(ci.Region) == 0 {
		return nil, errors.New("the relay map named no region")
	}
	return ci.Region[0], nil
}

// probeRelay asks each of a region's relays for an HTTPS response.
//
// It exists because the node's own Start succeeds with a relay that does not
// answer -- it connects lazily, in the background -- so without asking, a
// controller whose relay has gone would report a healthy listener that no host
// can reach. Any HTTP answer counts: a relay that speaks HTTPS on its port is
// one the node's own connection can reach.
func probeRelay(ctx context.Context, region *tailcfg.DERPRegion) error {
	if region == nil {
		return errors.New("no relay region")
	}
	// The whole region shares one budget: the listener's lock is held while
	// it is asked, and an enrolment request waits behind it.
	ctx, cancel := context.WithTimeout(ctx, 2*relayProbeTimeout)
	defer cancel()
	var last error
	for _, n := range region.Nodes {
		if n.STUNOnly {
			continue
		}
		host := n.HostName
		if n.IPv4 != "" && n.IPv4 != "none" {
			host = n.IPv4
		}
		port := n.DERPPort
		if port == 0 {
			port = 443
		}
		serverName := n.CertName
		if serverName == "" {
			serverName = n.HostName
		}
		client := &http.Client{
			Timeout: relayProbeTimeout,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				// InsecureForTests is only ever set by a relay map built in a
				// test; the public map never carries it.
				TLSClientConfig: &tls.Config{ServerName: serverName, InsecureSkipVerify: n.InsecureForTests, MinVersion: tls.VersionTLS12},
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(host, strconv.Itoa(port))+"/derp/probe", nil)
		if err != nil {
			last = err
			continue
		}
		resp, err := client.Do(req)
		client.CloseIdleConnections()
		if err != nil {
			last = fmt.Errorf("relay %s did not answer: %w", n.HostName, err)
			continue
		}
		_ = resp.Body.Close()
		return nil
	}
	if last == nil {
		last = errors.New("the relay region has no relay to connect to")
	}
	return last
}

func (p *privateConnections) expandFn() func(context.Context) (*tailcfg.DERPRegion, error) {
	if p.expand != nil {
		return p.expand
	}
	return expandRelay
}

func (p *privateConnections) probeFn() func(context.Context, *tailcfg.DERPRegion) error {
	if p.probe != nil {
		return p.probe
	}
	return probeRelay
}

// chooseRegion is the relay a sealed identity should run on: its own if that
// answers, otherwise whichever one a fresh expansion finds that does. The
// error is why neither would do, in which case the region returned is the
// sealed one -- the node still runs on it, because that is the relay the
// enrolled hosts will come back through when it recovers.
func (p *privateConnections) chooseRegion(ctx context.Context, sealed *tailcfg.DERPRegion) (*tailcfg.DERPRegion, error) {
	probe := p.probeFn()
	sealedErr := probe(ctx, sealed)
	if sealedErr == nil {
		return sealed, nil
	}
	alt, err := p.expandFn()(ctx)
	if err != nil {
		return sealed, fmt.Errorf("%v, and no other relay could be found: %v", sealedErr, err)
	}
	if err := probe(ctx, alt); err != nil {
		return sealed, fmt.Errorf("%v, and the nearest other relay did not answer either: %v", sealedErr, err)
	}
	return alt, nil
}

func sameRegion(a, b *tailcfg.DERPRegion) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// reportPrivateFault hands the listener's state to the controller, which owns
// the problems list, and logs the transitions -- once each way, rather than on
// every check, so an outage is two log lines and not three a minute.
func (s *Server) reportPrivateFault(err error) {
	was := s.ctrl.PrivateConnectionFault() != nil
	if err == nil {
		s.ctrl.SetPrivateConnectionFault(nil)
		if was {
			s.log.Info("the private connection's relay answers again")
		}
		return
	}
	s.ctrl.SetPrivateConnectionFault(&controller.PrivateConnectionFault{Since: s.ctrl.Now(), Reason: err.Error()})
	if !was {
		s.log.Warn("private hosts cannot reach this controller; retrying", "error", err)
	}
}

// ensureTailcat starts lazily when an operator asks for a private enrolment.
// The sealed identity lives in the normal database backup, so restarting the
// controller does not strand every lab that enrolled through it.
func (s *Server) ensureTailcat(ctx context.Context) (string, error) {
	p := &s.private
	p.mu.Lock()
	defer p.mu.Unlock()
	return s.ensureTailcatLocked(ctx)
}

func (s *Server) ensureTailcatLocked(ctx context.Context) (string, error) {
	p := &s.private
	if p.closed || !s.cfg().Server.TailcatEnabled || s.cfg().Security.DisableAuth {
		return "", errors.New("private connections are disabled; enable authentication and server.tailcat_enabled, then restart the controller")
	}
	if p.node != nil {
		return p.address, nil
	}
	if s.key == nil {
		return "", errors.New("private connections need the controller encryption key; check security.encryption_key_file")
	}
	stored, err := s.ctrl.Store().GetSetting(ctx, tailcatIdentitySetting)
	if err != nil {
		return "", errors.New("cannot read the private connection identity")
	}
	identity := tailcat.NewPrivateKey()
	if stored != "" {
		sealed, err := base64.StdEncoding.DecodeString(stored)
		if err != nil {
			return "", errors.New("cannot decode the private connection identity")
		}
		raw, err := s.key.Open(sealed)
		if err != nil {
			return "", errors.New("cannot decrypt the private connection identity; restore the matching encryption key")
		}
		if json.Unmarshal(raw, identity) != nil || identity.Private.IsZero() || identity.Public.PresharedKey.IsZero() {
			return "", errors.New("the saved private connection identity is incomplete")
		}
	}
	var region *tailcfg.DERPRegion
	var relayErr error
	if len(identity.Public.Region) == 0 {
		// A new identity: no host holds its address yet, so a relay that
		// cannot be found is a refused enrolment and nothing more.
		region, err = p.expandFn()(ctx)
		if err != nil {
			return "", errors.New("cannot reach a Tailcat relay; check outbound internet access and try again")
		}
		identity.Public.RegionID = 0
		identity.Public.Region = []*tailcfg.DERPRegion{region}
	} else {
		p.wanted = true
		p.sealed = identity.Public.Region[0]
		region, relayErr = p.chooseRegion(ctx, p.sealed)
	}
	if err := s.startNodeLocked(identity, region); err != nil {
		if stored == "" {
			return "", err
		}
		// The node would not start on the relay chosen above, so ask the
		// relay map afresh rather than retry the same one forever.
		alt, expandErr := p.expandFn()(ctx)
		if expandErr != nil || sameRegion(alt, region) || s.startNodeLocked(identity, alt) != nil {
			s.reportPrivateFault(err)
			return "", err
		}
		region, relayErr = alt, nil
	}
	if stored == "" {
		raw, err := json.Marshal(identity)
		if err == nil {
			var sealed []byte
			sealed, err = s.key.Seal(raw)
			if err == nil {
				err = s.ctrl.Store().SetSetting(ctx, tailcatIdentitySetting, base64.StdEncoding.EncodeToString(sealed), true)
			}
		}
		if err != nil {
			s.stopNodeLocked()
			return "", errors.New("cannot save the private connection identity")
		}
		p.wanted, p.sealed = true, region
	}
	s.reportPrivateFault(relayErr)
	return p.address, nil
}

// startNodeLocked starts a node for identity on region and serves the agent
// API through it. The address it records carries the region, so a node on a
// fallback relay hands out the address of that relay and not the sealed one.
func (s *Server) startNodeLocked(identity *tailcat.PrivateKey, region *tailcfg.DERPRegion) error {
	p := &s.private
	listener := &tunnelListener{connections: make(chan net.Conn), done: make(chan struct{})}
	node := &tailcat.Server{
		Key: identity.Private, PresharedKey: identity.Public.PresharedKey,
		Region: region, Logf: func(string, ...any) {},
		OnTCP: func(port uint16) func(net.Conn) {
			if port != 80 {
				return nil
			}
			return listener.offer
		},
	}
	if err := node.Start(); err != nil {
		_ = listener.Close()
		_ = node.Close()
		return errors.New("cannot start the private connection; check network-interface permissions and outbound internet access, then try again")
	}
	public := identity.Public
	public.RegionID = 0
	public.Region = []*tailcfg.DERPRegion{region}
	server := s.httpServer(context.Background())
	server.Handler = s.privateAgentHandler()
	p.node, p.listener, p.http, p.address, p.region = node, listener, server, string(public.Addr()), region
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("private connection listener stopped; restart the controller")
		}
	}()
	return nil
}

func (s *Server) stopNodeLocked() {
	p := &s.private
	if p.listener != nil {
		_ = p.listener.Close()
	}
	if p.http != nil {
		_ = p.http.Close()
	}
	if p.node != nil {
		_ = p.node.Close()
	}
	p.node, p.listener, p.http, p.address, p.region = nil, nil, nil, "", nil
}

// checkTailcat is one pass of the loop that keeps a wanted listener honest:
// it starts one that failed, says so while no relay answers, and moves the
// node back to the sealed relay -- or off it, when it is gone and another
// answers -- without a restart.
func (s *Server) checkTailcat(ctx context.Context) {
	p := &s.private
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.wanted {
		return
	}
	if p.node == nil {
		if _, err := s.ensureTailcatLocked(ctx); err != nil {
			s.reportPrivateFault(err)
		}
		return
	}
	region, err := p.chooseRegion(ctx, p.sealed)
	if err != nil || sameRegion(region, p.region) {
		s.reportPrivateFault(err)
		return
	}
	// A different relay answers than the one the node is on: the sealed one
	// has come back, or the one in use has gone and another has not.
	s.stopNodeLocked()
	if _, err := s.ensureTailcatLocked(ctx); err != nil {
		s.reportPrivateFault(err)
	}
}

// watchTailcat runs checkTailcat until ctx ends: every fast while there is a
// fault or the node is on a fallback relay, and every slow while it is healthy
// on the sealed one.
func (s *Server) watchTailcat(ctx context.Context, fast, slow time.Duration) {
	t := time.NewTimer(s.nextTailcatCheck(fast, slow))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.checkTailcat(ctx)
			t.Reset(s.nextTailcatCheck(fast, slow))
		}
	}
}

// nextTailcatCheck is how long to wait before the next check.
func (s *Server) nextTailcatCheck(fast, slow time.Duration) time.Duration {
	p := &s.private
	p.mu.Lock()
	healthy := p.node != nil && sameRegion(p.region, p.sealed)
	p.mu.Unlock()
	if healthy && s.ctrl.PrivateConnectionFault() == nil {
		return slow
	}
	return fast
}

func (s *Server) closeTailcat() {
	p := &s.private
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	s.stopNodeLocked()
}

// resumeTailcat brings back the listener of an identity that already exists,
// and starts the loop that keeps it honest until ctx ends.
//
// A listener that cannot start is the private hosts' problem, not the whole
// controller's: refusing to start would take the direct hosts, the UI and the
// webhooks down with it, and would need a restart to recover once the relay
// comes back. It is raised as tailcat.unavailable and retried instead, so the
// only error here is a database that cannot be read.
func (s *Server) resumeTailcat(ctx context.Context, fast, slow time.Duration) error {
	if !s.cfg().Server.TailcatEnabled || s.cfg().Security.DisableAuth {
		return nil
	}
	stored, err := s.ctrl.Store().GetSetting(ctx, tailcatIdentitySetting)
	if err != nil {
		return err
	}
	if stored != "" {
		s.private.mu.Lock()
		s.private.wanted = true
		s.private.mu.Unlock()
		if _, err := s.ensureTailcat(ctx); err != nil {
			s.reportPrivateFault(err)
		}
	}
	go s.watchTailcat(ctx, fast, slow)
	return nil
}
