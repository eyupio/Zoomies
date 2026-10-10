package assistant

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// lookupIP resolves a host for the dialer. It is a variable so a test can say
// what a name resolves to; production uses the system resolver.
var lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// The ranges that count as local beyond what netip answers itself. They
// are kept here rather than imported from internal/config/egress.go because
// this package may not depend on config (purity_test.go), and the two lists
// guard different promises: that one refuses an address being saved, this
// one refuses bytes leaving. They are not the same list on purpose: the
// NAT64 and 6to4 ranges egress.go refuses are addresses that get translated
// to a public one on the way out, which is the opposite of local, so a
// local-only dialer is right to let them through to the public-address rule.
var localPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("fec0::/10"),
}

// IsLocalAddress says whether an address is this machine, its link-local
// neighbourhood or a private network: the places a local model lives, and
// the only places local-only mode lets a request go.
func IsLocalAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	for _, p := range localPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// NewDialer returns the dial function the assistant's HTTP clients use. With
// localOnly set it resolves the host first and refuses to connect when any
// address the name resolves to is public, so the promise that a local model
// stays local is kept against the resolver's answer and not the name's look.
func NewDialer(localOnly bool) func(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if !localOnly {
			return dialer.DialContext(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addrs, err := lookupIP(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			if !IsLocalAddress(a) {
				return nil, fmt.Errorf("local-only mode refuses %s: it resolves to %s, which is not on this machine or a private network", host, a)
			}
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("local-only mode refuses %s: it resolves to nothing", host)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].String(), port))
	}
}
