package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/provider/proxmox"
	"github.com/eyupio/zoomies/internal/proxmoxsetup"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/tailscale/tailcat"
)

func setupCertificate(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pve"}, DNSNames: []string{"pve.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestProviderSetupSealsConnectionAndDoesNotReenrolRunner(t *testing.T) {
	h := newHarnessWithProviders(t, []provider.Factory{proxmox.NewFactory()})
	admin, _ := h.user("admin", store.RoleAdmin)
	viewer, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(admin)
	before := h.host("pve-1")
	before, _ = h.st.GetHost(h.ctx, before.ID)
	address := privateAddress(t)
	h.api.private.node = &tailcat.Server{}
	h.api.private.address = address
	defer func() { h.api.private.node = nil }()
	h.do(request{method: "POST", path: "/api/v1/provider-setups", cookie: h.session(viewer), body: map[string]any{}}).mustStatus(t, 403, "viewer")
	created := h.do(request{method: "POST", path: "/api/v1/provider-setups", cookie: cookie, body: map[string]any{}})
	created.mustStatus(t, 201, "create setup")
	var setup providerSetupView
	created.into(t, &setup)
	match := regexp.MustCompile(`--token '([0-9a-f]+)'`).FindStringSubmatch(setup.Command)
	if len(match) != 2 || !strings.Contains(setup.Command, "tailcat://"+address) {
		t.Fatal("missing private setup capability")
	}
	token := match[1]
	path := "/api/v1/provider-setups/" + setup.ID
	conn := proxmoxsetup.Connection{Name: "proxmox-pve-1", Endpoint: "https://pve.example:8006", Credential: "zoomies@pve!provider=secret", CAPEM: setupCertificate(t), TailcatAddress: address, Templates: []proxmoxsetup.Template{{VMID: 9000, Name: "runner", Node: "pve-1"}}}
	h.do(request{method: "POST", path: path + "/complete", cookie: cookie, body: conn}).mustStatus(t, 401, "session is not a setup token")
	h.do(request{method: "POST", path: path + "/complete", token: "wrong", body: conn}).mustStatus(t, 401, "wrong capability")
	h.do(request{method: "POST", path: path + "/complete", token: token, body: conn}).mustStatus(t, 204, "complete setup")
	h.do(request{method: "POST", path: path + "/complete", token: token, body: conn}).mustStatus(t, 204, "identical retry")
	altered := conn
	altered.Name = "another-host"
	h.do(request{method: "POST", path: path + "/complete", token: token, body: altered}).mustStatus(t, 401, "retry cannot replace connection")
	h.do(request{method: "GET", path: path, token: token}).mustStatus(t, 401, "capability cannot read user API")
	got := h.do(request{method: "GET", path: path, cookie: cookie})
	got.mustStatus(t, 200, "ready setup")
	var ready providerSetupView
	got.into(t, &ready)
	if !ready.Ready || ready.Name != conn.Name || ready.Endpoint != conn.Endpoint || ready.Command != "" || len(ready.Templates) != 1 || ready.Templates[0].VMID != 9000 {
		t.Fatalf("ready response: %+v", ready)
	}
	for _, secret := range []string{token, conn.Credential, address} {
		if strings.Contains(string(got.body), secret) {
			t.Fatal("setup response leaked a secret")
		}
	}
	stored, err := h.st.GetProviderSetup(h.ctx, setup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.PayloadEnc), conn.Credential) || strings.Contains(string(stored.TokenHash), token) {
		t.Fatal("setup persisted plaintext secrets")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/validate", nil)
	w := httptest.NewRecorder()
	badEndpoint := "https://attacker.example:8006"
	in := providerInput{SetupID: &setup.ID, Endpoint: &badEndpoint}
	if !h.api.resolveProviderSetup(w, req, &in) || *in.Endpoint != conn.Endpoint || *in.Credential != conn.Credential || *in.CAPEM != conn.CAPEM || *in.InsecureSkipVerify {
		t.Fatal("staged credential was not bound to its verified endpoint")
	}
	saved := h.do(request{method: "POST", path: "/api/v1/providers", cookie: cookie, body: map[string]any{
		"setup_id": setup.ID, "endpoint": badEndpoint,
		"settings": map[string]string{"nodes": "pve-1", "template_id": "9000", "storage": "local-lvm", "bridge": "vmbr0", "vmid_min": "9100", "vmid_max": "9199"},
	}})
	saved.mustStatus(t, 201, "save staged provider")
	var made providerResponse
	saved.into(t, &made)
	if !made.CredentialsConfigured || made.Connection != "tailcat" || made.Endpoint != conn.Endpoint || made.MaxMachines != 0 {
		t.Fatalf("saved provider: %+v", made)
	}
	for _, secret := range []string{token, conn.Credential, address} {
		if strings.Contains(string(saved.body), secret) {
			t.Fatal("provider response leaked a setup secret")
		}
	}
	if _, err := h.st.GetProviderSetup(h.ctx, setup.ID); err == nil {
		t.Fatal("saved provider retained its staged credential")
	}
	after, err := h.st.GetHost(h.ctx, before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("existing runner host changed")
	}
	hosts, _ := h.st.ListHosts(h.ctx)
	if len(hosts) != 1 {
		t.Fatal("provider setup enrolled another runner host")
	}
}

func TestProviderSetupPendingAndMissingDraftsAreRejected(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	created := h.do(request{method: "POST", path: "/api/v1/provider-setups", cookie: cookie, body: map[string]any{"controller_url": "https://zoomies.example"}})
	created.mustStatus(t, 201, "setup")
	var setup providerSetupView
	created.into(t, &setup)
	for _, path := range []string{"/api/v1/providers", "/api/v1/providers/validate", "/api/v1/providers/discover"} {
		h.do(request{method: "POST", path: path, cookie: cookie, body: map[string]any{"setup_id": setup.ID}}).mustStatus(t, 422, "pending setup")
		h.do(request{method: "POST", path: path, cookie: cookie, body: map[string]any{"setup_id": "pvs_missing"}}).mustStatus(t, 422, "missing setup")
	}
	h.do(request{method: "POST", path: "/api/v1/provider-setups", cookie: cookie, body: map[string]any{"controller_url": "http://zoomies.example"}}).mustStatus(t, 422, "plaintext credential upload")
}

func TestPrivateListenerOnlyAdmitsProviderCompletion(t *testing.T) {
	h := newHarness(t)
	handler := h.api.privateAgentHandler()
	for _, path := range []string{"/api/v1/providers", "/api/v1/provider-setups", "/api/v1/provider-setups/pvs_unknown"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 404 {
			t.Fatalf("private listener exposes %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/provider-setups/pvs_unknown/complete", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatalf("private completion must still authenticate its capability: %d", w.Code)
	}
}
