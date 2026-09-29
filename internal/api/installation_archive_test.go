package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// otherKey is a second instance key, so an import proves the archive did not
// lean on the exporting instance's.
const otherKey = "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="

// secondInstallation is the neighbour: its own target, its own pool and job,
// sealed with the same key as the first.
func (h *harness) secondInstallation(target string) *store.Installation {
	h.t.Helper()
	pem, _ := h.key.SealString("-----BEGIN RSA PRIVATE KEY-----\nother\n-----END RSA PRIVATE KEY-----")
	inst := &store.Installation{AppID: h.gh.AppID(), InstallationID: h.gh.InstallationID() + 1, Target: target,
		TargetType: store.TargetOrg, APIBaseURL: h.gh.URL(), PrivateKeyEnc: pem}
	if err := h.st.CreateInstallation(h.ctx, inst); err != nil {
		h.t.Fatalf("CreateInstallation: %v", err)
	}
	return inst
}

func (h *harness) installationRows(id string) string {
	h.t.Helper()
	tables, err := h.st.ExportInstallation(h.ctx, id)
	if err != nil {
		h.t.Fatalf("ExportInstallation: %v", err)
	}
	raw, _ := json.Marshal(tables)
	return string(raw)
}

// installationFigures renders one installation's usage report over a fixed
// window as JSON, so two readings compare byte for byte.
func (h *harness) installationFigures(id string, from, to time.Time) string {
	h.t.Helper()
	rows, err := h.st.Usage(h.ctx, from, to, store.UsageByInstallation)
	if err != nil {
		h.t.Fatalf("Usage: %v", err)
	}
	var out []store.UsageRow
	for _, r := range rows {
		if r.Key == id {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		h.t.Fatal("the installation has no figures, so comparing them would prove nothing")
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// The package's acceptance, end to end through the API: one installation is
// exported under a passphrase and purged, its neighbour's rows and figures do
// not move by a byte, and the archive imports onto a fresh instance with a
// different key and verifies against GitHub there.
func TestAnExportedInstallationPurgesCleanlyAndVerifiesOnAFreshInstance(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	a := h.installation()
	b := h.secondInstallation("globex")
	aPool, bPool := h.pool(a, "acme-linux"), h.pool(b, "globex-linux")
	aJob, _ := h.job(aPool, store.JobCompleted), h.job(bPool, store.JobCompleted)

	from, to := time.Now().Add(-24*time.Hour), time.Now().Add(time.Hour)
	beforeRows, beforeFigures := h.installationRows(b.ID), h.installationFigures(b.ID, from, to)

	res := h.do(request{method: http.MethodPost, path: "/api/v1/installations/" + a.ID + "/export",
		cookie: h.session(admin), body: map[string]any{"passphrase": "move it carefully"}})
	res.mustStatus(t, http.StatusOK, "export")
	archive := res.body
	if strings.Contains(string(archive), "BEGIN RSA") || strings.Contains(string(archive), b.ID) {
		t.Fatal("the archive carries a key in the clear, or names the other installation")
	}
	if !strings.Contains(string(archive), aJob.ID) {
		t.Fatal("the archive does not carry the installation's job")
	}

	// A purge is confirmed by typing the target, as a machine release is.
	res = h.do(request{method: http.MethodDelete, path: "/api/v1/installations/" + a.ID + "?purge=true",
		cookie: h.session(admin)})
	res.mustStatus(t, http.StatusUnprocessableEntity, "purge without confirmation")
	if _, err := h.st.GetInstallation(h.ctx, a.ID); err != nil {
		t.Fatalf("an unconfirmed purge removed the installation: %v", err)
	}
	res = h.do(request{method: http.MethodDelete, path: "/api/v1/installations/" + a.ID + "?purge=true&confirm=" + url.QueryEscape(a.Target),
		cookie: h.session(admin)})
	res.mustStatus(t, http.StatusOK, "purge")
	if _, err := h.st.GetJob(h.ctx, aJob.ID); err == nil {
		t.Error("the purged installation's job is still here")
	}
	if got := h.installationRows(b.ID); got != beforeRows {
		t.Errorf("the neighbour's rows changed:\nbefore %s\nafter  %s", beforeRows, got)
	}
	if got := h.installationFigures(b.ID, from, to); got != beforeFigures {
		t.Errorf("the neighbour's figures changed:\nbefore %+v\nafter  %+v", beforeFigures, got)
	}
	audit, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{TargetID: a.ID}, store.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	if !reflect.DeepEqual(actions, []string{"installation.purge"}) {
		t.Errorf("after the purge the audit rows naming it are %v, want only the purge itself", actions)
	}

	// The exporting installation points at the fake GitHub on loopback.
	fresh := newHarness(t, allowPrivateEgress, func(c *config.Config) { c.Security.EncryptionKey = otherKey })
	root, _ := fresh.user("root", store.RoleAdmin)
	res = fresh.do(request{method: http.MethodPost, path: "/api/v1/installations/import", cookie: fresh.session(root),
		body: map[string]any{"archive": json.RawMessage(archive), "passphrase": "the wrong one"}})
	res.mustStatus(t, http.StatusUnprocessableEntity, "import under the wrong passphrase")
	if list, _ := fresh.st.ListInstallations(fresh.ctx); len(list) != 0 {
		t.Fatal("a refused import left an installation behind")
	}
	res = fresh.do(request{method: http.MethodPost, path: "/api/v1/installations/import", cookie: fresh.session(root),
		body: map[string]any{"archive": json.RawMessage(archive), "passphrase": "move it carefully"}})
	res.mustStatus(t, http.StatusCreated, "import")
	if _, err := fresh.st.GetJob(fresh.ctx, aJob.ID); err != nil {
		t.Errorf("the job did not arrive with the installation: %v", err)
	}

	res = fresh.do(request{method: http.MethodPost, path: "/api/v1/installations/" + a.ID + "/verify", cookie: fresh.session(root)})
	res.mustStatus(t, http.StatusOK, "verify on the fresh instance")
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	res.into(t, &out)
	if !out.OK {
		t.Errorf("the imported installation did not verify: %s", out.Message)
	}
}

// Without a passphrase the history travels and the credential does not, and
// the import says the key has to be entered by hand by leaving it empty.
func TestAnExportWithoutAPassphraseCarriesNoCredential(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	a := h.installation()
	res := h.do(request{method: http.MethodPost, path: "/api/v1/installations/" + a.ID + "/export", cookie: h.session(admin)})
	res.mustStatus(t, http.StatusOK, "export")
	var doc struct {
		Secrets json.RawMessage `json:"secrets"`
		Tables  []struct {
			Table   string   `json:"table"`
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
		} `json:"tables"`
	}
	res.into(t, &doc)
	if doc.Secrets != nil {
		t.Error("an export with no passphrase carries secrets")
	}
	// The row's own secret columns never leave sealed under this instance's
	// key, passphrase or not: that ciphertext would outlive a purge.
	inst := doc.Tables[0]
	for i, c := range inst.Columns {
		if (c == "private_key_enc" || c == "webhook_secret_enc") && inst.Rows[0][i] != nil {
			t.Errorf("%s left the instance as %v", c, inst.Rows[0][i])
		}
	}
}

// The ordinary delete is unchanged: history stays behind, as it always has.
func TestDeletingAnInstallationWithoutPurgeKeepsItsHistory(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	a := h.installation()
	job := h.job(h.pool(a, "acme-linux"), store.JobCompleted)
	res := h.do(request{method: http.MethodDelete, path: "/api/v1/installations/" + a.ID, cookie: h.session(admin)})
	res.mustStatus(t, http.StatusOK, "delete")
	if _, err := h.st.GetJob(h.ctx, job.ID); err != nil {
		t.Errorf("a plain delete took the job with it: %v", err)
	}
}

// An archive is a hand-editable file and the import treats it as untrusted, but
// its installations row carries api_base_url, which wins over
// github.api_base_url for everything that installation does. Left unchecked, an
// import is the one way to set that field past the outbound address guard that
// create and update apply, and every GitHub call the installation makes then
// goes to the address the archive names.
func TestImportRefusesAnArchiveWhoseAPIBaseURLBreaksTheEgressGuard(t *testing.T) {
	source := newHarness(t)
	sourceAdmin, _ := source.user("root", store.RoleAdmin)
	a := source.installation()
	res := source.do(request{method: http.MethodPost, path: "/api/v1/installations/" + a.ID + "/export", cookie: source.session(sourceAdmin)})
	res.mustStatus(t, http.StatusOK, "export")
	exported := res.body

	// withBaseURL rewrites the api_base_url cell of the archive's installations
	// row, as an operator editing the file by hand would.
	withBaseURL := func(t *testing.T, value any) json.RawMessage {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(exported, &doc); err != nil {
			t.Fatal(err)
		}
		for _, tbl := range doc["tables"].([]any) {
			tbl := tbl.(map[string]any)
			if tbl["table"] != "installations" {
				continue
			}
			for i, c := range tbl["columns"].([]any) {
				if c == "api_base_url" {
					tbl["rows"].([]any)[0].([]any)[i] = value
					raw, _ := json.Marshal(doc)
					return raw
				}
			}
		}
		t.Fatal("the archive has no installations.api_base_url column to edit")
		return nil
	}

	for _, tc := range []struct {
		name    string
		value   any
		allowed bool
		status  int
	}{
		{"the metadata address", "http://169.254.169.254/api/v3/", false, http.StatusUnprocessableEntity},
		{"loopback", "http://127.0.0.1:9/api/v3/", false, http.StatusUnprocessableEntity},
		{"a value that is not text", 42, false, http.StatusUnprocessableEntity},
		{"loopback with private egress allowed", "http://127.0.0.1:9/api/v3/", true, http.StatusCreated},
		{"a public Enterprise host", "https://ghes.example.com/api/v3/", false, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := []func(*config.Config){func(c *config.Config) { c.Security.EncryptionKey = otherKey }}
			if tc.allowed {
				opts = append(opts, allowPrivateEgress)
			}
			fresh := newHarness(t, opts...)
			root, _ := fresh.user("root", store.RoleAdmin)
			res := fresh.do(request{method: http.MethodPost, path: "/api/v1/installations/import", cookie: fresh.session(root),
				body: map[string]any{"archive": withBaseURL(t, tc.value)}})
			if res.status != tc.status {
				t.Fatalf("status = %d, want %d: %s", res.status, tc.status, res.body)
			}
			list, _ := fresh.st.ListInstallations(fresh.ctx)
			if tc.status != http.StatusCreated {
				if !strings.Contains(string(res.body), "api_base_url") {
					t.Errorf("the refusal must name the field:\n%s", res.body)
				}
				if len(list) != 0 {
					t.Error("a refused import left an installation behind")
				}
			}
		})
	}
}
