package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/eyupio/zoomies/internal/backup"
	"github.com/eyupio/zoomies/internal/store"
)

func operatorCookie(h *harness) string {
	h.t.Helper()
	u, _ := h.user("instance-operator-"+store.NewID(store.PrefixUser), store.Roles()[len(store.Roles())-1])
	return h.session(u)
}

func TestPreparingAndExportingAnInstanceNeedsOperatorAuthority(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("fleet-admin", store.RoleAdmin)
	cookie := h.session(u)
	for _, path := range []string{"/api/v1/transfers/preparation", "/api/v1/transfers/export"} {
		r := h.do(request{method: http.MethodPost, path: path, cookie: cookie, body: map[string]string{"passphrase": "a long transfer passphrase"}})
		r.mustStatus(t, http.StatusForbidden, "fleet administrator transfer request")
	}
	cookie = operatorCookie(h)
	unfenced := h.do(request{method: http.MethodPost, path: "/api/v1/transfers/export", cookie: cookie, body: map[string]string{"passphrase": "a long transfer passphrase"}})
	unfenced.mustStatus(t, http.StatusConflict, "unprepared export")
	prepare := h.do(request{method: http.MethodPost, path: "/api/v1/transfers/preparation", cookie: cookie})
	prepare.mustStatus(t, http.StatusAccepted, "one-click preparation")
	var p store.TransferProgress
	prepare.into(t, &p)
	if !p.Ready {
		t.Fatal("empty instance not ready", p)
	}
	exported := h.do(request{method: http.MethodPost, path: "/api/v1/transfers/export", cookie: cookie, body: map[string]string{"passphrase": "a long transfer passphrase"}})
	exported.mustStatus(t, http.StatusOK, "complete export")
	encrypted, _ := backup.IsEncrypted(bytes.NewReader(exported.body))
	if !encrypted {
		t.Fatal("export not encrypted")
	}
}

func TestAPortableUploadUsesDestinationKeyAndStagesOnlyAfterSourceStop(t *testing.T) {
	source := newHarness(t)
	source.installation()
	cookie := operatorCookie(source)
	source.do(request{method: http.MethodPost, path: "/api/v1/transfers/preparation", cookie: cookie}).mustStatus(t, http.StatusAccepted, "prepare source")
	exported := source.do(request{method: http.MethodPost, path: "/api/v1/transfers/export", cookie: cookie, body: map[string]string{"passphrase": "a long transfer passphrase"}})
	exported.mustStatus(t, http.StatusOK, "export source")
	dest := newHarness(t)
	destCookie := operatorCookie(dest)
	if err := dest.st.Backup(dest.ctx, dest.ctrl.DatabasePath()); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("passphrase", "a long transfer passphrase")
	part, err := mw.CreateFormFile("file", "instance.zbk")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(exported.body)
	_ = mw.Close()
	uploaded := dest.do(request{method: http.MethodPost, path: "/api/v1/transfers/import", cookie: destCookie, rawBody: body.String(), headers: map[string]string{"Content-Type": mw.FormDataContentType()}})
	uploaded.mustStatus(t, http.StatusCreated, "import portable archive")
	var entry backupView
	uploaded.into(t, &entry)
	if entry.Transfer == nil || !entry.Transfer.Prepared || entry.KeyMatches == nil || !*entry.KeyMatches {
		t.Fatalf("not prepared: %+v", entry)
	}
	stagePath := "/api/v1/backups/" + entry.ID + "/restore"
	dest.do(request{method: http.MethodPost, path: stagePath, cookie: destCookie}).mustStatus(t, http.StatusUnprocessableEntity, "stage without source stop")
	staged := dest.do(request{method: http.MethodPost, path: stagePath, cookie: destCookie, body: map[string]bool{"source_stopped": true}})
	staged.mustStatus(t, http.StatusAccepted, "stage complete import")
	var pending backup.Staged
	if err = json.Unmarshal(staged.body, &pending); err != nil {
		t.Fatal(err)
	}
	if !pending.SourceStopped {
		t.Fatal("source acknowledgement lost")
	}
	installs, err := dest.st.ListInstallations(dest.ctx)
	if err != nil || len(installs) != 0 {
		t.Fatal("staging changed the live fleet", err)
	}
}
