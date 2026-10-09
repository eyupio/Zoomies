package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/backup"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// The backups surface.
//
// Everything here is a thin layer over internal/backup, which is the same
// code `zoomies backup` and `zoomies restore` run: the settings page takes,
// lists, verifies, downloads, uploads and deletes the copies in the backup
// directory, and stages a restore for the next start. It never restores under
// the running controller, because nothing can -- see backup.Stage.

// maxBackupUploadBytes bounds an uploaded archive. It is far above any
// database this controller has met and far below "fill the disk": an upload
// is unpacked beside the live database, on the volume the fleet runs from.
const maxBackupUploadBytes = 8 << 30

// backupView is one backup as the page renders it.
//
// The manifest's facts are lifted out flat rather than nested, because the
// table has a column for each and a client should not have to know the
// manifest's shape to draw one. The manifest itself is not carried: it holds
// the blanked configuration, which is a page of JSON per row.
type backupView struct {
	Transfer *backup.TransferInfo `json:"transfer,omitempty"`
	ID       string               `json:"id"`
	TakenAt  time.Time            `json:"taken_at"`
	Source   string               `json:"source"`
	TakenBy  string               `json:"taken_by,omitempty"`
	Location string               `json:"location"`
	// Bytes is the database; TotalBytes is the directory, which differs when
	// the key is in it.
	Bytes      int64 `json:"bytes"`
	TotalBytes int64 `json:"total_bytes"`
	// Version is the build that wrote it, and SchemaLatest the newest
	// migration the copy had taken.
	Version          string `json:"version,omitempty"`
	SchemaLatest     string `json:"schema_latest,omitempty"`
	SchemaMigrations int    `json:"schema_migrations"`
	Integrity        string `json:"integrity,omitempty"`
	// Secrets is how many sealed credentials the key is needed for.
	Secrets int `json:"secrets"`
	// KeyFingerprint is the key that sealed it; KeyMatches says whether this
	// host holds that key, and is null when the backup does not say.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	KeyMatches     *bool  `json:"key_matches"`
	KeyIncluded    bool   `json:"key_included"`
	// Restorable says every check a restore would make on this backup
	// passes from what the manifest says; RestoreProblem is the one that
	// does not. A backup with no manifest is restorable with a caveat.
	Restorable     bool   `json:"restorable"`
	RestoreProblem string `json:"restore_problem,omitempty"`
	// Problem is something wrong with the backup itself.
	Problem string `json:"problem,omitempty"`
}

// The two places a backup can be.
const (
	locationBackups      = "backups"
	locationPreMigration = "pre-migration"
)

// backupsResponse is the whole tab in one document.
type backupsResponse struct {
	Items     []backupView `json:"items"`
	Directory string       `json:"directory"`
	// DatabaseBytes is the live database's size, which is roughly what a
	// backup costs; DiskFreeBytes is the room beside it, when the platform
	// can say.
	DatabaseBytes int64  `json:"database_bytes"`
	DiskFreeBytes *int64 `json:"disk_free_bytes"`
	// KeyFingerprint is this host's key, for the row that says whether a
	// backup was sealed with it.
	KeyFingerprint string         `json:"key_fingerprint,omitempty"`
	Schedule       backupSchedule `json:"schedule"`
	// Remotes is every destination the copies are sent to, and what became
	// of each. Empty is the default and says so on the page: a fleet whose
	// backups never leave the host should be told that, not left to assume
	// otherwise from a tab full of tidy rows.
	Remotes []controller.RemoteBackupStatus `json:"remotes"`
	// Running says a backup is being taken right now.
	Running bool `json:"running"`
	// StagedRestore is the restore waiting for a restart, if any;
	// LastRestore is what became of the last one applied.
	StagedRestore *backup.Staged  `json:"staged_restore"`
	LastRestore   *backup.Outcome `json:"last_restore"`
	// Restarting says this process has been asked to stop and is on its way
	// down, so a page that receives this should start waiting for the next.
	Restarting bool `json:"restarting"`
}

// backupSchedule is the controller's own schedule, as the page shows it.
type backupSchedule struct {
	Enabled         bool       `json:"enabled"`
	Interval        string     `json:"interval"`
	Keep            int        `json:"keep"`
	NextDueAt       *time.Time `json:"next_due_at"`
	LastScheduledAt *time.Time `json:"last_scheduled_at"`
	LastScheduledID string     `json:"last_scheduled_id,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

// restoreRequest is what staging a restore asks for.
type restoreRequest struct {
	SourceStopped    bool `json:"source_stopped"`
	RevokeAPITokens  bool `json:"revoke_api_tokens"`
	ResetAgentTokens bool `json:"reset_agent_tokens"`
}

// downloadRequest is the body of the encrypted download.
type downloadRequest struct {
	Passphrase string `json:"passphrase"`
}

// restartResponse answers the request that stops the controller.
type restartResponse struct {
	Restarting bool   `json:"restarting"`
	Message    string `json:"message"`
}

// minPassphrase is the shortest passphrase the encrypted download accepts.
// Eight characters of argon2id is not a strong secret, but a limit that
// refuses "1234" stops the one case that is no protection at all.
//
// It is the validator's number rather than a second one, because the same
// archive is sealed with the same cipher whether a browser asked for it or a
// backup remote is being written to, and two limits that disagree would make
// the page and the startup warning say different things about one passphrase.
const minPassphrase = config.MinBackupPassphrase

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (s *Server) backupView(e backup.Entry, location string) backupView {
	v := backupView{
		ID: e.ID, TakenAt: e.TakenAt, Source: e.Source, Location: location,
		Bytes: e.Bytes, TotalBytes: e.TotalBytes, KeyIncluded: e.KeyIncluded, Problem: e.Problem,
	}
	if m := e.Manifest; m != nil {
		v.Transfer = m.Transfer
		v.TakenBy = m.TakenBy
		v.Version = m.Zoomies.Version
		v.SchemaLatest = m.LastMigration()
		v.SchemaMigrations = len(m.Database.Migrations)
		v.Integrity = m.Database.Integrity
		v.Secrets = len(m.Secrets)
		v.KeyFingerprint = m.Key.Fingerprint
		if m.Key.Fingerprint != "" {
			matches := s.key != nil && s.key.Fingerprint() == m.Key.Fingerprint
			v.KeyMatches = &matches
		}
	}
	v.Restorable, v.RestoreProblem = s.restorable(e)
	return v
}

// restorable answers from the manifest what backup.Check would answer from
// the file, so the list can grey out the rows a restore would refuse without
// opening every database in the directory.
func (s *Server) restorable(e backup.Entry) (bool, string) {
	if e.Problem != "" {
		return false, e.Problem
	}
	m := e.Manifest
	if m == nil {
		return true, "there is no manifest, so the encryption key and the release that wrote it cannot be checked before restoring; the database itself will be"
	}
	known := store.KnownMigrations()
	for _, name := range m.Database.Migrations {
		if !slices.Contains(known, name) {
			return false, fmt.Sprintf("written by a newer release: this build does not have the migration %s", name)
		}
	}
	if m.Key.Fingerprint != "" {
		if s.key == nil {
			return false, "this backup needs the encryption key " + m.Key.Fingerprint + ", and this controller has no key"
		}
		if s.key.Fingerprint() != m.Key.Fingerprint {
			return false, fmt.Sprintf("sealed with the encryption key %s, and this controller's key is %s; restoring would give a fleet that cannot authenticate to GitHub", m.Key.Fingerprint, s.key.Fingerprint())
		}
	}
	return true, ""
}

// findBackup resolves an id in the backup directory, then among the store's
// pre-migration copies. The backup directory wins a collision: it is where
// the operator's own copies are.
func (s *Server) findBackup(id string) (*backup.Entry, string, error) {
	if !backup.ValidID(id) {
		return nil, "", backup.ErrInvalidID
	}
	e, err := backup.Get(s.ctrl.BackupDir(), id)
	if err == nil {
		return e, locationBackups, nil
	}
	if !errors.Is(err, backup.ErrNotFound) {
		return nil, "", err
	}
	e, err = backup.Get(backup.PreMigrationDir(s.ctrl.DatabasePath()), id)
	if err != nil {
		return nil, "", err
	}
	return e, locationPreMigration, nil
}

// failBackup renders the package's own refusals: an id that is not one is a
// 400 rather than a 404, because it was never going to name anything.
func (s *Server) failBackup(w http.ResponseWriter, r *http.Request, doing string, err error) {
	switch {
	case errors.Is(err, backup.ErrInvalidID):
		badRequestField(w, "id", "that is not a backup id; one looks like zoomies-20260916-120000")
	case errors.Is(err, backup.ErrNotFound):
		notFound(w, "there is no backup with that id; it may have been removed since the page loaded")
	case errors.Is(err, controller.ErrBackupRunning):
		conflict(w, err.Error())
	default:
		s.internal(w, r, doing, err)
	}
}

// ---------------------------------------------------------------------------
// Listing and taking
// ---------------------------------------------------------------------------

// handleListBackups answers GET /api/v1/backups.
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	dir := s.ctrl.BackupDir()
	dbPath := s.ctrl.DatabasePath()
	items := []backupView{}
	own, err := backup.List(dir)
	if err != nil {
		s.internal(w, r, "listing the backups", err)
		return
	}
	for _, e := range own {
		items = append(items, s.backupView(e, locationBackups))
	}
	pre, err := backup.List(backup.PreMigrationDir(dbPath))
	if err != nil {
		s.internal(w, r, "listing the pre-migration copies", err)
		return
	}
	for _, e := range pre {
		items = append(items, s.backupView(e, locationPreMigration))
	}
	slices.SortStableFunc(items, func(a, b backupView) int { return b.TakenAt.Compare(a.TakenAt) })

	out := backupsResponse{Items: items, Directory: dir, StagedRestore: nil, LastRestore: nil}
	if info, err := os.Stat(dbPath); err == nil {
		out.DatabaseBytes = info.Size()
	}
	// The directory may not exist until the first backup; its parent is on
	// the same filesystem, and is where the space would come from.
	probe := dir
	if _, err := os.Stat(probe); err != nil {
		probe = filepath.Dir(dir)
	}
	if free, ok := backup.DiskFree(probe); ok {
		out.DiskFreeBytes = &free
	}
	if s.key != nil {
		out.KeyFingerprint = s.key.Fingerprint()
	}
	status := s.ctrl.BackupStatus()
	out.Running = status.Running
	out.Remotes = s.ctrl.BackupRemotes(r.Context())
	out.Schedule = backupSchedule{
		Enabled:         status.Interval > 0,
		Interval:        config.Text(config.Setting{Kind: config.KindDuration}, status.Interval),
		Keep:            status.Keep,
		LastScheduledID: status.LastScheduledID,
		LastError:       status.LastError,
	}
	if !status.NextDueAt.IsZero() {
		at := status.NextDueAt
		out.Schedule.NextDueAt = &at
	}
	if !status.LastScheduledAt.IsZero() {
		at := status.LastScheduledAt
		out.Schedule.LastScheduledAt = &at
	}
	if staged, err := backup.LoadStaged(dbPath); err == nil {
		out.StagedRestore = staged
	} else {
		s.logger(r).Warn("could not read the staged restore", "error", err)
	}
	if last, err := backup.LastOutcome(dbPath); err == nil {
		out.LastRestore = last
	} else {
		s.logger(r).Warn("could not read the last restore's outcome", "error", err)
	}
	out.Restarting, _ = s.ctrl.Restarting()
	writeJSON(w, http.StatusOK, out)
}

// handleGetBackup answers GET /api/v1/backups/{id}.
func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	e, location, err := s.findBackup(chiURLParam(r, "id"))
	if err != nil {
		s.failBackup(w, r, "reading the backup", err)
		return
	}
	writeJSON(w, http.StatusOK, s.backupView(*e, location))
}

// handleTakeBackup answers POST /api/v1/backups: a copy, now, on behalf of
// whoever pressed the button.
func (s *Server) handleTakeBackup(w http.ResponseWriter, r *http.Request) {
	id := Identity(r.Context())
	e, err := s.ctrl.TakeBackup(r.Context(), backup.SourceManual, id.Name)
	if err != nil {
		s.failBackup(w, r, "taking a backup", err)
		return
	}
	s.ctrl.NoteManualBackup()
	s.auth.Auditor().Act(r.Context(), id, "backup.take", "backup", e.ID, map[string]any{
		"bytes": e.Bytes, "dir": e.Dir,
	})
	writeJSON(w, http.StatusCreated, s.backupView(*e, locationBackups))
}

// handleDeleteBackup answers DELETE /api/v1/backups/{id}.
func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	e, location, err := s.findBackup(chiURLParam(r, "id"))
	if err != nil {
		s.failBackup(w, r, "reading the backup", err)
		return
	}
	// A backup that is about to be restored must not be deleted from under
	// the restart that would apply it.
	if staged, _ := backup.LoadStaged(s.ctrl.DatabasePath()); staged != nil && staged.BackupID == e.ID {
		conflict(w, "this backup is staged to be restored at the next restart; cancel the restore first")
		return
	}
	if err := os.RemoveAll(e.Dir); err != nil {
		s.internal(w, r, "removing the backup", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "backup.delete", "backup", e.ID, map[string]any{
		"taken_at": e.TakenAt, "source": e.Source, "location": location, "bytes": e.Bytes,
	})
	noContent(w)
}

// handleVerifyBackup answers POST /api/v1/backups/{id}/verify.
//
// A POST because it reads the whole file: on a large database it is seconds
// of work, and a GET that costs seconds is one a page will make by accident.
func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request) {
	e, _, err := s.findBackup(chiURLParam(r, "id"))
	if err != nil {
		s.failBackup(w, r, "reading the backup", err)
		return
	}
	v, err := backup.Verify(r.Context(), filepath.Dir(e.Dir), e.ID)
	if err != nil {
		s.failBackup(w, r, "verifying the backup", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---------------------------------------------------------------------------
// Download and upload
// ---------------------------------------------------------------------------

// handleDownloadBackup answers GET /api/v1/backups/{id}/download: the backup
// as a gzipped tar, and POST with a passphrase: the same, encrypted.
//
// The download is audited, which few reads are, because it is the whole
// database leaving the machine.
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	e, _, err := s.findBackup(chiURLParam(r, "id"))
	if err != nil {
		s.failBackup(w, r, "reading the backup", err)
		return
	}
	passphrase := ""
	if r.Method == http.MethodPost {
		var body downloadRequest
		if !decode(w, r, &body) {
			return
		}
		if len(body.Passphrase) < minPassphrase {
			unprocessable(w, "the passphrase is too short", []fieldError{{"passphrase",
				fmt.Sprintf("use at least %d characters; the archive is only as private as this", minPassphrase)}})
			return
		}
		passphrase = body.Passphrase
	}

	name := backup.ArchiveName(e.ID, passphrase != "")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	// A large backup over a slow link takes as long as it takes.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)

	var out io.Writer = w
	if passphrase != "" {
		enc, err := backup.NewEncryptor(w, passphrase)
		if err != nil {
			s.logger(r).Warn("could not start the encrypted download", "backup", e.ID, "error", err)
			return
		}
		defer func() { _ = enc.Close() }()
		out = enc
	}
	if err := backup.WriteArchive(out, e); err != nil {
		// The headers are gone; the client gets a short file, and the log
		// gets the reason.
		s.logger(r).Warn("a backup download did not complete", "backup", e.ID, "error", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "backup.download", "backup", e.ID, map[string]any{
		"encrypted": passphrase != "", "bytes": e.TotalBytes, "key_included": e.KeyIncluded,
	})
}

// handleUploadBackup answers POST /api/v1/backups/upload: a multipart form
// with the archive in `file` and, for an encrypted one, its `passphrase`.
//
// Multipart rather than a raw body, because the passphrase has to travel with
// the file and a header or a query string is where a passphrase ends up in a
// proxy log. The file part is spooled to disk before anything is decided, so
// the passphrase may come before or after it.
func (s *Server) handleUploadBackup(w http.ResponseWriter, r *http.Request) {
	s.uploadSnapshot(w, r, false)
}

func (s *Server) handleImportTransfer(w http.ResponseWriter, r *http.Request) {
	s.uploadSnapshot(w, r, true)
}

func (s *Server) uploadSnapshot(w http.ResponseWriter, r *http.Request, transfer bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		badRequest(w, "send the backup as multipart/form-data: a `file` part holding the archive, and a `passphrase` part when it is encrypted")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUploadBytes)
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "the request is not a multipart form: "+err.Error())
		return
	}

	root := s.ctrl.BackupDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		s.internal(w, r, "creating the backup directory", err)
		return
	}
	spool, err := os.CreateTemp(root, ".incoming-*")
	if err != nil {
		s.internal(w, r, "spooling the upload", err)
		return
	}
	defer os.Remove(spool.Name())
	defer spool.Close()

	var passphrase, filename string
	var received bool
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				payloadTooLarge(w, fmt.Sprintf("the upload is larger than the %d byte limit", maxErr.Limit))
				return
			}
			badRequest(w, "reading the upload: "+err.Error())
			return
		}
		switch part.FormName() {
		case "passphrase":
			raw, err := io.ReadAll(io.LimitReader(part, 4097))
			if err != nil {
				badRequest(w, "reading the passphrase: "+err.Error())
				return
			}
			if len(raw) > 4096 {
				badRequest(w, "the passphrase exceeds 4096 bytes")
				return
			}
			passphrase = string(raw)
		case "file":
			if received {
				badRequest(w, "the form has more than one file part; send one archive per request")
				return
			}
			received = true
			filename = part.FileName()
			if _, err := io.Copy(spool, part); err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					payloadTooLarge(w, fmt.Sprintf("the upload is larger than the %d byte limit", maxErr.Limit))
					return
				}
				s.internal(w, r, "spooling the upload", err)
				return
			}
		default:
			// Ignored rather than refused: a form with an extra field is a
			// client that will be fixed, not an attack.
		}
		part.Close()
	}
	if !received {
		badRequest(w, "the form has no `file` part")
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		s.internal(w, r, "reading the spooled upload", err)
		return
	}

	var src io.Reader = spool
	encrypted, peek := backup.IsEncrypted(spool)
	src = peek
	switch {
	case encrypted && passphrase == "":
		unprocessable(w, "this archive is encrypted", []fieldError{{"passphrase", "it was downloaded with a passphrase; send the same one"}})
		return
	case encrypted && !transfer:
		src, err = backup.NewDecryptor(peek, passphrase)
		if err != nil {
			s.internal(w, r, "opening the encrypted upload", err)
			return
		}
	case passphrase != "":
		// Not an error: an operator who typed a passphrase for a plain
		// archive has lost nothing. The upload proceeds without it.
	}

	var entry *backup.Entry
	if transfer {
		entry, err = backup.ReadTransfer(r.Context(), s.cfg(), root, peek, passphrase)
	} else {
		entry, err = backup.Unpack(r.Context(), root, src, backup.UnpackOptions{
			Source: backup.SourceUploaded, TakenBy: Identity(r.Context()).Name, MaxBytes: maxBackupUploadBytes, Now: s.ctrl.Now,
		})
		if err == nil && entry.Manifest != nil && entry.Manifest.Transfer != nil {
			_ = os.RemoveAll(entry.Dir)
			err = errors.New("use the complete instance transfer import endpoint for this archive")
		}
	}
	if err != nil {
		if errors.Is(err, backup.ErrWrongPassphrase) {
			unprocessable(w, "the archive did not open", []fieldError{{"passphrase", backup.ErrWrongPassphrase.Error()}})
			return
		}
		unprocessable(w, "that file is not a Zoomies backup this controller can use", []fieldError{{"file", strings.TrimPrefix(err.Error(), "backup: ")}})
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "backup.upload", "backup", entry.ID, map[string]any{
		"filename": filename, "encrypted": encrypted, "bytes": entry.Bytes, "taken_at": entry.TakenAt,
	})
	writeJSON(w, http.StatusCreated, s.backupView(*entry, locationBackups))
}

// ---------------------------------------------------------------------------
// Restoring
// ---------------------------------------------------------------------------

// handleStageRestore answers POST /api/v1/backups/{id}/restore.
//
// Nothing is restored here. The controller cannot swap the database it is
// running on, so the request is checked -- every check a restore makes -- and
// written down for the next controller to apply before it opens anything. The
// response is the staged restore; the restart that applies it is a separate,
// deliberate act.
func (s *Server) handleStageRestore(w http.ResponseWriter, r *http.Request) {
	e, _, err := s.findBackup(chiURLParam(r, "id"))
	if err != nil {
		s.failBackup(w, r, "reading the backup", err)
		return
	}
	var body restoreRequest
	if !decodeOptional(w, r, &body) {
		return
	}
	if existing, _ := backup.LoadStaged(s.ctrl.DatabasePath()); existing != nil && existing.BackupID != e.ID {
		conflict(w, fmt.Sprintf("%s is already staged to be restored; cancel that first", existing.BackupID))
		return
	}
	id := Identity(r.Context())
	staged, err := backup.Stage(r.Context(), s.cfg(), e, backup.Staged{
		SourceStopped: body.SourceStopped, RequestedBy: id.Name, RequestedAt: s.ctrl.Now(),
		RevokeAPITokens: body.RevokeAPITokens, ResetAgentTokens: body.ResetAgentTokens,
	})
	if err != nil {
		// Every refusal here is one the operator can act on: the wrong key,
		// a newer release, a copy that is not sound.
		unprocessable(w, "this backup cannot be restored", []fieldError{{"id", err.Error()}})
		return
	}
	s.auth.Auditor().Act(r.Context(), id, "backup.restore_staged", "backup", e.ID, map[string]any{
		"taken_at": staged.TakenAt, "revoke_api_tokens": staged.RevokeAPITokens, "reset_agent_tokens": staged.ResetAgentTokens,
	})
	writeJSON(w, http.StatusAccepted, staged)
}

// handleCancelRestore answers DELETE /api/v1/backups/restore.
func (s *Server) handleCancelRestore(w http.ResponseWriter, r *http.Request) {
	dbPath := s.ctrl.DatabasePath()
	staged, err := backup.LoadStaged(dbPath)
	if err != nil {
		s.internal(w, r, "reading the staged restore", err)
		return
	}
	if err := backup.CancelStaged(dbPath); err != nil {
		s.internal(w, r, "cancelling the staged restore", err)
		return
	}
	if staged != nil {
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "backup.restore_cancelled", "backup", staged.BackupID, nil)
	}
	noContent(w)
}

// handleDismissRestoreOutcome answers DELETE /api/v1/backups/restore/outcome:
// the operator has read what became of the last restore.
func (s *Server) handleDismissRestoreOutcome(w http.ResponseWriter, r *http.Request) {
	if err := backup.ClearOutcome(s.ctrl.DatabasePath()); err != nil {
		s.internal(w, r, "dismissing the last restore's outcome", err)
		return
	}
	noContent(w)
}

// handleApplyRestore answers POST /api/v1/backups/restore/apply: stop this
// controller so that its service manager starts the next, which applies the
// staged restore before it opens the database.
//
// It refuses when nothing is staged, because a restart with nothing to apply
// is an outage somebody asked for by mistake.
func (s *Server) handleApplyRestore(w http.ResponseWriter, r *http.Request) {
	staged, err := backup.LoadStaged(s.ctrl.DatabasePath())
	if err != nil {
		s.internal(w, r, "reading the staged restore", err)
		return
	}
	if staged == nil {
		conflict(w, "no restore is staged; choose a backup and stage its restore first")
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "backup.restore_applied", "backup", staged.BackupID, map[string]any{
		"requested_by": staged.RequestedBy, "taken_at": staged.TakenAt,
	})
	s.ctrl.RequestRestart("restoring " + staged.BackupID + ", asked for by " + Identity(r.Context()).Name)
	writeJSON(w, http.StatusAccepted, restartResponse{
		Restarting: true,
		Message: "The controller is stopping. If its service manager starts it again, the restore is applied before the database is opened " +
			"and the fleet comes back fenced; if nothing starts it, start it by hand and the same happens.",
	})
}

// The encrypted file is finished privately before response headers are sent,
// so a failed export has a JSON error and cannot be mistaken for a snapshot.
func (s *Server) handleExportTransfer(w http.ResponseWriter, r *http.Request) {
	var body downloadRequest
	if !decode(w, r, &body) {
		return
	}
	if len(body.Passphrase) < config.MinBackupPassphrase || len(body.Passphrase) > 4096 {
		unprocessable(w, "choose a transfer passphrase", []fieldError{{"passphrase", fmt.Sprintf("use between %d and 4096 characters", config.MinBackupPassphrase)}})
		return
	}
	if err := s.ctrl.Store().TransferReady(r.Context()); err != nil {
		conflict(w, err.Error())
		return
	}
	root := s.ctrl.BackupDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		s.internal(w, r, "creating the transfer directory", err)
		return
	}
	f, err := os.CreateTemp(root, ".outgoing-*")
	if err != nil {
		s.internal(w, r, "creating the encrypted transfer", err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = backup.WriteTransfer(r.Context(), s.ctrl.Store(), s.cfg(), body.Passphrase, f); err != nil {
		s.internal(w, r, "preparing the encrypted transfer", err)
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		s.internal(w, r, "reading the encrypted transfer", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "instance.export", "instance", "", map[string]any{"encrypted": true})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="zoomies-instance.zbk"`)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if _, err = io.Copy(w, f); err != nil {
		s.logger(r).Warn("complete instance download did not finish", "error", err)
	}
}
