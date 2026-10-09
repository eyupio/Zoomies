package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// AllowPrivateEgressSetting is the key that lets the addresses below be dialled,
// named once because every refusal has to quote it.
const AllowPrivateEgressSetting = "security.allow_private_egress"

// AllowPrivateProviderSetting and LocalOnlySetting are the assistant's two
// switches, named once because the refusals and the settings page quote them.
const (
	AllowPrivateProviderSetting = "assistant.allow_private_provider"
	LocalOnlySetting            = "assistant.local_only"
)

// The ranges an outbound URL may not name. Go's netip answers loopback,
// link-local and RFC 1918 / ULA itself; these are the neighbours it has no
// predicate for, and each is somewhere a request from this process lands on
// infrastructure nobody meant to hand to a form field.
var (
	thisNetwork  = netip.MustParsePrefix("0.0.0.0/8")      // "this host on this network": Linux dials 0.0.0.0 as loopback
	sharedCGNAT  = netip.MustParsePrefix("100.64.0.0/10")  // carrier-grade NAT, and the range overlay networks hand out
	benchmarking = netip.MustParsePrefix("198.18.0.0/15")  // reserved for benchmarking, used as a private range in practice
	reservedV4   = netip.MustParsePrefix("240.0.0.0/4")    // reserved, including the limited broadcast address
	v4Compatible = netip.MustParsePrefix("::/96")          // deprecated IPv4-compatible IPv6, :: and ::1 among them
	siteLocal    = netip.MustParsePrefix("fec0::/10")      // deprecated site-local, still routed privately by some stacks
	nat64        = netip.MustParsePrefix("64:ff9b::/96")   // well-known NAT64: the last 32 bits are the IPv4 destination
	nat64Local   = netip.MustParsePrefix("64:ff9b:1::/48") // local-use NAT64, same embedding
	sixToFour    = netip.MustParsePrefix("2002::/16")      // 6to4: bits 16-48 are the IPv4 destination
)

// loopbackNames are host names that resolve to this machine on any system
// that has an /etc/hosts, or that a cloud resolves to its metadata service.
// They are refused as names because the check below never resolves anything.
var loopbackNames = map[string]string{
	"localhost":                "this machine",
	"localhost.localdomain":    "this machine",
	"ip6-localhost":            "this machine",
	"ip6-loopback":             "this machine",
	"metadata.google.internal": "the cloud metadata service",
}

// CheckOutboundURL refuses a URL this process would dial on an operator's
// behalf when it names this machine, its link-local neighbourhood or a private
// network, and returns nil when it does not or when allowPrivate is set.
//
// It exists because a setting that holds a URL is a setting that makes the
// controller send a request wherever it points. An administrator who may set
// oidc.issuer may otherwise point it at 169.254.169.254 and read the cloud
// credentials of the machine the controller runs on out of the error message,
// or at a service on the LAN that trusts anything from inside it. Those are
// the platform's resources, not the fleet's, which is why the switch that
// permits them is platform-scoped.
//
// The check reads the address it is given and resolves nothing. A validator
// cannot resolve DNS honestly: the answer at startup need not be the answer at
// the next request, and a name the operator does not control can be pointed at
// 10.0.0.1 afterwards. So an IP literal and the handful of names that can only
// be local are refused, and any other host name is admitted on trust.
// docs/security.md says so rather than letting this read as a network policy.
//
// setting names the key being checked, so the finding points at the value to
// change as well as at the switch that would allow it. The finding is an
// error, which is what a write through the API refuses with; the validator
// lowers it to a warning, because a value in the file is the process owner's
// and an upgrade must not stop an install that works.
func CheckOutboundURL(setting, raw string, allowPrivate bool) *Finding {
	if allowPrivate {
		return nil
	}
	host, ok := outboundHost(raw)
	if !ok {
		// Not a URL at all is a different mistake, and each caller already
		// has its own finding for it.
		return nil
	}
	what := privateTarget(host)
	if what == "" {
		return nil
	}
	return &Finding{
		Code:     "egress.private_target",
		Severity: SeverityError,
		Setting:  setting,
		Title:    fmt.Sprintf("%s points at %s, which is %s", setting, host, what),
		Detail: "this controller would send a request there on a configuration's say-so. An address on this machine, its " +
			"link-local network or a private range reaches services that trust whatever is inside the network -- the " +
			"cloud metadata service, an admin port bound to loopback, the LAN -- and none of them are the fleet's to reach.",
		Fix: fmt.Sprintf("use the service's public address; or, if it really does live on a network you own (an identity "+
			"provider or Enterprise Server on the LAN, a hypervisor beside this host), set %s to true.", AllowPrivateEgressSetting),
	}
}

// CheckProviderURL is CheckOutboundURL for an assistant provider's address,
// with the assistant's own switch as the fix: a model on this machine is the
// ordinary case there, and the finding must not send an operator to the
// blanket switch that would open every other URL with it.
func CheckProviderURL(raw string, allowPrivate bool) *Finding {
	f := CheckOutboundURL("base_url", raw, allowPrivate)
	if f == nil {
		return nil
	}
	f.Setting = AllowPrivateProviderSetting
	f.Fix = fmt.Sprintf("use a hosted provider's public address; or, for a model that really does run on this machine or "+
		"on a network you own, set %s to true.", AllowPrivateProviderSetting)
	return f
}

// outboundHost pulls the host out of what an operator typed, normalised the
// way a dialler will read it. A bare host:port gets a scheme first, because
// url.Parse would otherwise read "10.0.0.5:8006" as a scheme and a path and
// the check would see no host at all -- and the Proxmox driver accepts
// exactly that spelling.
func outboundHost(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if !strings.Contains(s, "://") {
		s = "https://" + strings.TrimPrefix(s, "//")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	// A trailing dot is the fully qualified spelling of the same name, and
	// "localhost." resolves exactly where "localhost" does.
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", false
	}
	return host, true
}

// privateTarget says what host is when it is somewhere an outbound request
// must not go, and "" when it is a public address or a host name this check
// cannot see through.
func privateTarget(host string) string {
	if what, ok := loopbackNames[host]; ok {
		return what
	}
	if strings.HasSuffix(host, ".localhost") {
		// RFC 6761 reserves the whole of .localhost for this machine.
		return "this machine"
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		if numericHost(host) {
			// 2130706433, 0x7f000001 and 127.1 are all 127.0.0.1 to a URL
			// parser that follows the WHATWG rules and to C's inet_aton, and
			// netip reads none of them. Nothing public is written like this,
			// so an address this check cannot read is refused rather than
			// waved through as though it were a name.
			return "an IP address written in a form this check does not read"
		}
		return ""
	}
	return addressTarget(addr.WithZone(""))
}

// PrivateAddress says what an address is when it is somewhere an outbound
// request must not go, and "" when it is public. It is the same judgement
// CheckOutboundURL makes of a literal, offered for the dialler's side: code
// that resolves a name and checks the address it is about to connect to
// must not keep a second, shorter list of ranges that drifts from this one.
func PrivateAddress(addr netip.Addr) string {
	return addressTarget(addr.WithZone(""))
}

// addressTarget classifies one IP address.
func addressTarget(addr netip.Addr) string {
	// ::ffff:10.0.0.1 is 10.0.0.1 on the wire; judge the address it carries.
	addr = addr.Unmap()
	switch {
	case addr.IsUnspecified() || (addr.Is4() && thisNetwork.Contains(addr)):
		return "the unspecified address, which reaches this machine"
	case addr.IsLoopback():
		return "a loopback address on this machine"
	case addr.IsLinkLocalUnicast():
		if addr == netip.AddrFrom4([4]byte{169, 254, 169, 254}) {
			return "the cloud metadata service's link-local address"
		}
		return "a link-local address"
	case addr.IsPrivate():
		return "a private-network address"
	case addr.Is4() && sharedCGNAT.Contains(addr):
		return "a carrier-grade NAT (100.64.0.0/10) address"
	case addr.Is4() && benchmarking.Contains(addr):
		return "an address in the reserved benchmarking range"
	case addr.Is4() && reservedV4.Contains(addr):
		return "a reserved or broadcast address"
	case addr.IsMulticast():
		return "a multicast address, which is not one host"
	case addr.Is6() && v4Compatible.Contains(addr):
		return "a deprecated IPv4-compatible address"
	case addr.Is6() && siteLocal.Contains(addr):
		return "a site-local address"
	case addr.Is6() && (nat64.Contains(addr) || nat64Local.Contains(addr)):
		b := addr.As16()
		if what := addressTarget(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})); what != "" {
			return what + " behind a NAT64 prefix"
		}
	case addr.Is6() && sixToFour.Contains(addr):
		b := addr.As16()
		if what := addressTarget(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})); what != "" {
			return what + " inside a 6to4 address"
		}
	}
	return ""
}

// numericHost reports whether a host that did not parse as an address is
// nonetheless one: its last label is a number, decimal, octal or 0x-hex. A
// DNS name's top-level label is never numeric, which is the rule the WHATWG
// URL standard uses to decide a host is IPv4.
func numericHost(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if last == "" {
		return false
	}
	digits := last
	if strings.HasPrefix(last, "0x") {
		digits = last[2:]
		for _, r := range digits {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
		return true
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// OutboundURL is one setting whose value this process dials.
type OutboundURL struct {
	Setting string
	Value   string
}

// OutboundURLs lists the settings the controller sends requests to, so the
// validator and the API's settings write ask about the same ones. An issuer
// is listed only while single sign-on is on, because nothing dials it
// otherwise and a draft value should not be refused or warned about.
func (c *Config) OutboundURLs() []OutboundURL {
	out := []OutboundURL{
		{"github.api_base_url", c.GitHub.APIBaseURL},
		{"capacity_demand.destination_url", c.CapacityDemand.DestinationURL},
		{"agent.runner_download_url", c.Agent.RunnerDownloadURL},
	}
	if c.OIDC.Enabled {
		out = append(out, OutboundURL{"oidc.issuer", c.OIDC.Issuer})
	}
	return out
}
