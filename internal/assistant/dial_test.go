package assistant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
)

// Local-only is a promise about where bytes go, so it is kept after
// resolution: a name that looks private and resolves to a public address is
// the attack, not the edge case. The resolver is a package variable so a
// test can say what a name resolves to without touching DNS.
func TestLocalOnlyRefusesAnAddressThatResolvesToAPublicAddress(t *testing.T) {
	stubResolver(t, map[string][]netip.Addr{"public.example": {netip.MustParseAddr("93.184.216.34")}})
	dial := NewDialer(true)
	for _, addr := range []string{"93.184.216.34:443", "public.example:443"} {
		_, err := dial(context.Background(), "tcp", addr)
		if err == nil || !strings.Contains(err.Error(), "local-only") {
			t.Errorf("%s: err %v, want a local-only refusal", addr, err)
		}
	}
}

func TestLocalOnlyDialsLoopbackAndPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client := &http.Client{Transport: &http.Transport{DialContext: NewDialer(true)}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, ip := range []string{"10.0.0.1", "192.168.1.1", "172.16.0.1", "127.0.0.1", "::1", "fd00::1", "169.254.1.1"} {
		if !IsLocalAddress(netip.MustParseAddr(ip)) {
			t.Errorf("%s should count as local", ip)
		}
	}
	for _, ip := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "8.8.8.8"} {
		if IsLocalAddress(netip.MustParseAddr(ip)) {
			t.Errorf("%s should not count as local", ip)
		}
	}
}

func TestWithoutLocalOnlyTheDialerDialsWhatItIsGiven(t *testing.T) {
	// A public name resolved to loopback on a closed port: the dial reaches
	// the connect and is refused there, which is the proof it was attempted.
	stubResolver(t, map[string][]netip.Addr{"public.example": {netip.MustParseAddr("127.0.0.1")}})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	_, err = NewDialer(false)(context.Background(), "tcp", net.JoinHostPort("public.example", itoa(port)))
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Errorf("err %v, want the connect to be refused", err)
	}
}

func stubResolver(t *testing.T, table map[string][]netip.Addr) {
	t.Helper()
	old := lookupIP
	lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if a, ok := table[host]; ok {
			return a, nil
		}
		return old(ctx, host)
	}
	t.Cleanup(func() { lookupIP = old })
}

func itoa(n int) string { return strconv.Itoa(n) }
