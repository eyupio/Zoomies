package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// maxInstallationArchiveBytes bounds an import. An archive is one
// installation's history -- a year of sessions and whatever jobs retention
// kept -- so it can outgrow the user API's one-megabyte body limit by a long
// way, and is still nothing like a whole database.
const maxInstallationArchiveBytes = 512 << 20

type exportInstallationRequest struct {
	// Passphrase re-seals the App's private key and webhook secret so the
	// archive can be imported elsewhere without this instance's key. It is in
	// the body, not the query, because a query string is where a passphrase
	// ends up in a proxy log.
	Passphrase string `json:"passphrase,omitempty"`
}

// handleExportInstallation answers POST /api/v1/installations/{id}/export.
func (s *Server) handleExportInstallation(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	var req exportInstallationRequest
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if req.Passphrase != "" && s.key == nil {
		s.noEncryptionKey(w)
		return
	}
	archive, err := s.ctrl.ExportInstallation(r.Context(), id, req.Passphrase)
	if err != nil {
		s.fail(w, r, "exporting the installation", err)
		return
	}
	rows := map[string]int{}
	for _, t := range archive.Tables {
		rows[t.Table] = len(t.Rows)
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "installation.export", "installation", id, map[string]any{
		"target": archive.Target, "rows": rows, "credentials": archive.Secrets != nil,
	})
	raw, err := json.Marshal(archive)
	if err != nil {
		s.internal(w, r, "rendering the archive", err)
		return
	}
	stamp := archive.ExportedAt.Format("20060102-150405")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="zoomies-installation-%s-%s.json"`,
		safeFilePart(archive.Target), stamp))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(raw, '\n'))
}

// safeFilePart keeps a target name fit for a Content-Disposition filename.
func safeFilePart(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, s)
}

type importInstallationRequest struct {
	Archive    *controller.InstallationArchive `json:"archive"`
	Passphrase string                          `json:"passphrase,omitempty"`
}

type importInstallationResponse struct {
	Installation controller.InstallationView `json:"installation"`
	Rows         map[string]int              `json:"rows"`
	Skipped      map[string]int              `json:"skipped"`
}

// handleImportInstallation answers POST /api/v1/installations/import.
func (s *Server) handleImportInstallation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxInstallationArchiveBytes)
	var req importInstallationRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Archive == nil {
		unprocessable(w, "send the archive zoomies export wrote as `archive`", []fieldError{{"archive", "required"}})
		return
	}
	if msg := s.archiveEgressRefusal(req.Archive); msg != "" {
		unprocessable(w, "this archive cannot be imported", []fieldError{{"api_base_url", msg}})
		return
	}
	inst, done, err := s.ctrl.ImportInstallation(r.Context(), req.Archive, req.Passphrase)
	switch {
	case errors.Is(err, controller.ErrArchive), errors.Is(err, store.ErrArchiveShape):
		unprocessable(w, "this archive cannot be imported: "+strings.TrimPrefix(err.Error(), "installation archive: "), nil)
		return
	case err != nil:
		s.fail(w, r, "importing the installation", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "installation.import", "installation", inst.ID, map[string]any{
		"target": inst.Target, "rows": done.Rows, "skipped": done.Skipped,
		"exported_from": req.Archive.ExportedFrom, "credentials": req.Archive.Secrets != nil,
	})
	s.ctrl.Nudge()
	counts, cerr := s.ctrl.PoolCountsByInstallation(r.Context())
	if cerr != nil {
		s.internal(w, r, "reading the installation back", cerr)
		return
	}
	writeJSON(w, http.StatusCreated, importInstallationResponse{
		Installation: controller.NewInstallationView(inst, counts[inst.ID]),
		Rows:         done.Rows,
		Skipped:      done.Skipped,
	})
}

// archiveEgressRefusal says why an archive's installations row may not be
// imported, or "" when it may. The row's api_base_url is inserted as written
// and wins over github.api_base_url for everything the installation does, so
// an archive -- a file anyone can edit -- must meet the same outbound address
// guard as create and update, or the import is the way round it.
func (s *Server) archiveEgressRefusal(archive *controller.InstallationArchive) string {
	for _, t := range archive.Tables {
		if t.Table != "installations" {
			continue
		}
		col := -1
		for i, c := range t.Columns {
			if c == "api_base_url" {
				col = i
			}
		}
		if col < 0 {
			continue
		}
		for _, row := range t.Rows {
			if col >= len(row) || row[col].V == nil {
				continue
			}
			raw, ok := row[col].V.(string)
			if !ok {
				return "the archive's api_base_url must be a URL as text, such as https://ghes.example.com/api/v3; correct it in the file, or export the installation again"
			}
			if strings.TrimSpace(raw) == "" {
				continue
			}
			normalised, err := github.NormalizeAPIBaseURL(raw)
			if err != nil {
				return err.Error()
			}
			if msg := s.egressRefusal(normalised); msg != "" {
				return msg
			}
		}
	}
	return ""
}
