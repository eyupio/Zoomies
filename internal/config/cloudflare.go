package config

import "strings"

// TrustedProxyCloudflare is the whole word an operator writes in
// server.trusted_proxies when Cloudflare is in front. It expands to
// Cloudflare's published edge ranges, so the configuration says what it
// means and nobody has to copy two dozen CIDRs by hand -- or keep them
// current by hand.
const TrustedProxyCloudflare = "cloudflare"

// CloudflareCIDRs are Cloudflare's published edge ranges, as listed at
// https://www.cloudflare.com/ips/ on 2026-09-05. They are embedded rather
// than fetched at startup: a trust boundary must not depend on a third-party
// fetch succeeding, and a stale list fails safe -- a connection from a range
// this binary does not know is treated as an ordinary client, and its
// forwarded headers are ignored.
var CloudflareCIDRs = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

// ExpandTrustedProxies replaces the cloudflare token with Cloudflare's
// published ranges and passes everything else through untouched.
func ExpandTrustedProxies(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		if strings.TrimSpace(raw) == TrustedProxyCloudflare {
			out = append(out, CloudflareCIDRs...)
			continue
		}
		out = append(out, raw)
	}
	return out
}
