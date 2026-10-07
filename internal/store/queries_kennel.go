package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidKennelEvaluation is what SaveKennelEvaluation returns for a record
// it will not keep: JSON that does not parse, or a document over the limit.
// Both are guarded at the write because of how they fail later. SQLite's
// json_each aborts a whole query on a malformed document, so one bad row would
// stop the Overview counting every other repository.
var ErrInvalidKennelEvaluation = errors.New("store: kennel evaluation refused")

// MaxKennelDocumentBytes bounds each of a repository's stored JSON documents.
// An evaluation is a few findings of a few hundred bytes; sixty-four kibibytes
// is room for far more than any check produces, and a limit is what stops a
// bug from storing a repository's worth of text in a row.
const MaxKennelDocumentBytes = 64 << 10

// KennelRepository is one repository Kennel Club has looked at: who it is, and
// the last evaluation made of it.
type KennelRepository struct {
	ID           string
	GitHubHost   string
	RepositoryID int64
	// InstallationID is the installation that reads it now.
	InstallationID string
	FullName       string
	// Visibility is empty until GitHub has said.
	Visibility string
	// State is the last evaluation's standing -- pending until one has landed.
	State            string
	EvaluatorVersion int
	EvaluatedAt      *time.Time
	// NextDueAt is when the remote reads are next due. Zero means now.
	NextDueAt    time.Time
	InputsDigest string
	// Coverage, Evaluation and Watermark are JSON the controller writes and
	// reads. The store keeps them whole and validates them; it has no opinion
	// about what is in them.
	Coverage     json.RawMessage
	Evaluation   json.RawMessage
	Watermark    json.RawMessage
	OpenErrors   int
	OpenWarnings int
	OpenInfos    int
	Waived       int
	LastServedAt time.Time
}

// KennelRepositoryRef is what is known about a repository before it has been
// evaluated.
type KennelRepositoryRef struct {
	GitHubHost     string
	RepositoryID   int64
	InstallationID string
	FullName       string
	// Visibility is "" when this read did not learn it; a stored value is then
	// kept.
	Visibility string
}

// KennelEvaluationRecord is one evaluation, ready to keep.
type KennelEvaluationRecord struct {
	State            string
	EvaluatorVersion int
	EvaluatedAt      time.Time
	NextDueAt        time.Time
	InputsDigest     string
	Coverage         json.RawMessage
	Evaluation       json.RawMessage
	Watermark        json.RawMessage
	OpenErrors       int
	OpenWarnings     int
	OpenInfos        int
	Waived           int
}

const kennelCols = `id, github_host, repository_id, installation_id, full_name, visibility, state,
	evaluator_version, evaluated_at, next_due_at, inputs_digest, coverage_json, evaluation_json,
	watermark_json, open_errors, open_warnings, open_infos, waived, last_served_at`

func scanKennelRepository(sc interface{ Scan(...any) error }) (*KennelRepository, error) {
	var (
		r                            KennelRepository
		evaluated                    sql.NullInt64
		due, served                  int64
		coverage, evaluation, margin string
	)
	if err := sc.Scan(&r.ID, &r.GitHubHost, &r.RepositoryID, &r.InstallationID, &r.FullName, &r.Visibility,
		&r.State, &r.EvaluatorVersion, &evaluated, &due, &r.InputsDigest, &coverage, &evaluation, &margin,
		&r.OpenErrors, &r.OpenWarnings, &r.OpenInfos, &r.Waived, &served); err != nil {
		return nil, err
	}
	r.EvaluatedAt = atp(evaluated)
	r.NextDueAt = at(due)
	r.LastServedAt = at(served)
	r.Coverage, r.Evaluation, r.Watermark = json.RawMessage(coverage), json.RawMessage(evaluation), json.RawMessage(margin)
	return &r, nil
}

// TouchKennelRepository records that a repository is being served, creating its
// row the first time and keeping it from being pruned after that. A change of
// visibility makes the repository due at once: a repository that has just gone
// public is the one a stale answer is worst for.
func (s *Store) TouchKennelRepository(ctx context.Context, ref KennelRepositoryRef) (*KennelRepository, error) {
	if ref.GitHubHost == "" || ref.RepositoryID <= 0 || ref.InstallationID == "" || ref.FullName == "" {
		return nil, fmt.Errorf("kennel repository needs a host, a repository ID, an installation and a name: %w", ErrInvalidKennelEvaluation)
	}
	now := ms(s.Now())
	_, err := s.exec(ctx, `INSERT INTO kennel_repositories
		(id, github_host, repository_id, installation_id, full_name, visibility, last_served_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(github_host, repository_id) DO UPDATE SET
			installation_id = excluded.installation_id,
			full_name       = excluded.full_name,
			next_due_at     = CASE WHEN excluded.visibility <> '' AND excluded.visibility <> visibility
			                       THEN 0 ELSE next_due_at END,
			visibility      = CASE WHEN excluded.visibility <> '' THEN excluded.visibility ELSE visibility END,
			last_served_at  = excluded.last_served_at`,
		NewID(PrefixKennelRepository), ref.GitHubHost, ref.RepositoryID, ref.InstallationID, ref.FullName, ref.Visibility, now)
	if err != nil {
		return nil, wrapWrite(err)
	}
	r, err := scanKennelRepository(s.read.QueryRowContext(ctx,
		`SELECT `+kennelCols+` FROM kennel_repositories WHERE github_host = ? AND repository_id = ?`,
		ref.GitHubHost, ref.RepositoryID))
	if err != nil {
		// A reader that has not seen the write yet is a race this store does not
		// have: the writer and the reader share one WAL, and exec has returned.
		return nil, err
	}
	return r, nil
}

// GetKennelRepository returns one repository by its Kennel Club ID.
func (s *Store) GetKennelRepository(ctx context.Context, id string) (*KennelRepository, error) {
	r, err := scanKennelRepository(s.read.QueryRowContext(ctx,
		`SELECT `+kennelCols+` FROM kennel_repositories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("kennel repository %s: %w", id, ErrNotFound)
	}
	return r, err
}

// KennelFilter narrows the repository list.
type KennelFilter struct {
	// Q is a case-insensitive substring of the repository's name.
	Q              string
	States         []string
	InstallationID string
	// Code keeps repositories with an open finding of this code. The caller
	// validates it against the registry; it is bound as a parameter either way.
	Code string
	// Severity keeps repositories with at least one open finding of this severity
	// ("error", "warning" or "info"). The caller validates it; a value the store
	// does not know keeps nothing, so a mistake cannot read as "everything".
	Severity string
	// Served, when set, keeps the repositories the fleet has had a hand in a job
	// for since ServedSince (true) or the ones it has not (false). It asks the
	// jobs table the question KennelServedRepos does, so "active" means here what
	// it means to the loop that decides which repositories to look at, and not what
	// a row's last_served_at says: that is when a pass last saw the repository in
	// that list, and under the installation scope every pass sees every one.
	Served      *bool
	ServedSince time.Time
}

var kennelSortCols = map[string]string{
	"name":         "full_name COLLATE NOCASE",
	"evaluated_at": "COALESCE(evaluated_at, 0)",
	"severity":     "(open_errors * 1000000 + open_warnings * 1000 + open_infos)",
}

func kennelWhere(f KennelFilter) (string, []any) {
	var cond []string
	var args []any
	if f.Q != "" {
		cond = append(cond, `full_name LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(f.Q)+"%")
	}
	if len(f.States) > 0 {
		cond = append(cond, `state IN (`+strings.TrimSuffix(strings.Repeat("?,", len(f.States)), ",")+`)`)
		for _, st := range f.States {
			args = append(args, st)
		}
	}
	if f.InstallationID != "" {
		cond = append(cond, `installation_id = ?`)
		args = append(args, f.InstallationID)
	}
	if f.Served != nil {
		// By name, folded: a job records the repository as GitHub spelled it when
		// the webhook arrived, and the row as the listing did, and the loop already
		// matches the two without regard to case.
		in := "IN"
		if !*f.Served {
			in = "NOT IN"
		}
		cond = append(cond, `LOWER(full_name) `+in+` (SELECT LOWER(repo) FROM jobs
			WHERE queued_at >= ? AND repo <> '' AND installation_id <> '' AND `+managedJobSQL("jobs")+`)`)
		args = append(args, ms(f.ServedSince))
	}
	if f.Code != "" {
		cond = append(cond, `EXISTS (SELECT 1 FROM json_each(evaluation_json, '$.findings')
			WHERE json_extract(value, '$.code') = ?)`)
		args = append(args, f.Code)
	}
	switch f.Severity {
	case "":
	case "error":
		cond = append(cond, `open_errors > 0`)
	case "warning":
		cond = append(cond, `open_warnings > 0`)
	case "info":
		cond = append(cond, `open_infos > 0`)
	default:
		cond = append(cond, `0 = 1`)
	}
	if len(cond) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(cond, " AND "), args
}

// ListKennelRepositories returns a filtered page and the matching total. The
// default order puts the repository with the most errors first, then warnings,
// then notes, then by name, which is the order an operator wants to read it in.
func (s *Store) ListKennelRepositories(ctx context.Context, f KennelFilter, p Page) ([]*KennelRepository, int, error) {
	where, args := kennelWhere(f)
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM kennel_repositories `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := p.orderBy(kennelSortCols, "open_errors DESC, open_warnings DESC, open_infos DESC, full_name COLLATE NOCASE ASC")
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+kennelCols+` FROM kennel_repositories `+where+` ORDER BY `+order+`, id ASC LIMIT ? OFFSET ?`,
		append(args, p.limit(50, 500), max(p.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*KennelRepository
	for rows.Next() {
		r, err := scanKennelRepository(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// DueKennelRepositories returns the repositories whose remote reads are due,
// the longest-overdue first.
func (s *Store) DueKennelRepositories(ctx context.Context, limit int) ([]*KennelRepository, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+kennelCols+` FROM kennel_repositories WHERE next_due_at <= ? ORDER BY next_due_at, id LIMIT ?`,
		ms(s.Now()), min(max(limit, 1), 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*KennelRepository
	for rows.Next() {
		r, err := scanKennelRepository(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveKennelEvaluation keeps an evaluation. It refuses a document that is not
// valid JSON or is over MaxKennelDocumentBytes, and changes nothing if it does.
func (s *Store) SaveKennelEvaluation(ctx context.Context, id string, e KennelEvaluationRecord) error {
	for name, doc := range map[string]json.RawMessage{"coverage": e.Coverage, "evaluation": e.Evaluation, "watermark": e.Watermark} {
		if len(doc) > MaxKennelDocumentBytes {
			return fmt.Errorf("%s document is %d bytes, over the %d limit: %w", name, len(doc), MaxKennelDocumentBytes, ErrInvalidKennelEvaluation)
		}
		if !json.Valid(doc) {
			// An empty document is not valid JSON either, so this is the one rule for both.
			return fmt.Errorf("%s document is empty or not valid JSON: %w", name, ErrInvalidKennelEvaluation)
		}
	}
	res, err := s.exec(ctx, `UPDATE kennel_repositories SET
		state = ?, evaluator_version = ?, evaluated_at = ?, next_due_at = ?, inputs_digest = ?,
		coverage_json = ?, evaluation_json = ?, watermark_json = ?,
		open_errors = ?, open_warnings = ?, open_infos = ?, waived = ?
		WHERE id = ?`,
		e.State, e.EvaluatorVersion, ms(e.EvaluatedAt), ms(e.NextDueAt), e.InputsDigest,
		string(e.Coverage), string(e.Evaluation), string(e.Watermark),
		e.OpenErrors, e.OpenWarnings, e.OpenInfos, e.Waived, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("kennel repository %s: %w", id, ErrNotFound)
	}
	return nil
}

// RequestKennelRecheck makes a repository due now. It is what Recheck does: the
// loop, not the request, makes the reads, so a person pressing the button waits
// on the same budget as everything else.
func (s *Store) RequestKennelRecheck(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `UPDATE kennel_repositories SET next_due_at = 0 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("kennel repository %s: %w", id, ErrNotFound)
	}
	return nil
}

// PruneKennelRepositories deletes the repositories last served before the
// cutoff, and their waivers with them, and returns the IDs it removed so that
// each can be announced: a page that is looking at one is looking at something
// that no longer exists. It is the one delete Kennel Club makes, and only of its
// own rows.
func (s *Store) PruneKennelRepositories(ctx context.Context, before time.Time) ([]string, error) {
	var ids []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		ids, err = deletedIDs(ctx, tx, `DELETE FROM kennel_repositories WHERE last_served_at < ? RETURNING id`, ms(before))
		return err
	})
	return ids, err
}

// KennelRepositoryIDs lists the IDs of an installation's repositories, which is
// what the controller announces as gone before it deletes the installation: the
// rows cascade away with it, and a cascade says nothing on the event stream.
func (s *Store) KennelRepositoryIDs(ctx context.Context, installationID string) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id FROM kennel_repositories WHERE installation_id = ? ORDER BY id`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// KennelCounts is the Overview's numbers, counted over every repository.
type KennelCounts struct {
	Repositories int
	ByState      map[string]int
	OpenErrors   int
	OpenWarnings int
	OpenInfos    int
	Waived       int
	// OldestEvaluation is the longest-ago evaluation, nil if none has landed.
	OldestEvaluation *time.Time
}

// KennelCounts counts the repositories by state and their open findings by
// severity.
func (s *Store) KennelCounts(ctx context.Context) (*KennelCounts, error) {
	c := &KennelCounts{ByState: map[string]int{}}
	rows, err := s.read.QueryContext(ctx, `SELECT state, COUNT(*), SUM(open_errors), SUM(open_warnings),
		SUM(open_infos), SUM(waived) FROM kennel_repositories GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n, e, w, i, wv int
		if err := rows.Scan(&state, &n, &e, &w, &i, &wv); err != nil {
			return nil, err
		}
		c.ByState[state] = n
		c.Repositories += n
		c.OpenErrors += e
		c.OpenWarnings += w
		c.OpenInfos += i
		c.Waived += wv
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var oldest sql.NullInt64
	if err := s.read.QueryRowContext(ctx,
		`SELECT MIN(evaluated_at) FROM kennel_repositories WHERE evaluated_at IS NOT NULL`).Scan(&oldest); err != nil {
		return nil, err
	}
	c.OldestEvaluation = atp(oldest)
	return c, nil
}

// KennelCheckCounts says, for each check code, how many repositories have an
// open finding of it. A repository with two findings of one code counts once.
func (s *Store) KennelCheckCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT json_extract(f.value, '$.code'), COUNT(DISTINCT r.id)
		FROM kennel_repositories r, json_each(r.evaluation_json, '$.findings') f
		GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var code sql.NullString
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		if code.Valid {
			out[code.String] = n
		}
	}
	return out, rows.Err()
}

// KennelCoverageCounts says, for each source, how many repositories are in each
// state of reading it: {"runs": {"ok": 3, "denied": 1}}. It is what the
// Overview's coverage panel is made from, and counts a repository once per
// source it has a state for. A repository with no evaluation yet has no states
// and is in none of them.
func (s *Store) KennelCoverageCounts(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT c.key, json_extract(c.value, '$.state'), COUNT(*)
		FROM kennel_repositories r, json_each(r.coverage_json) c
		GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var source string
		var state sql.NullString
		var n int
		if err := rows.Scan(&source, &state, &n); err != nil {
			return nil, err
		}
		if !state.Valid {
			continue
		}
		if out[source] == nil {
			out[source] = map[string]int{}
		}
		out[source][state.String] = n
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Waivers
// ---------------------------------------------------------------------------

// KennelWaiver is a recorded decision that a finding is acceptable here.
type KennelWaiver struct {
	ID            string
	RepositoryPK  string
	Code          string
	Subject       string
	Severity      string
	Reason        string
	CreatedBy     string
	CreatedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

const kennelWaiverCols = `id, repository_pk, code, subject, severity, reason, created_by, created_by_name, created_at, expires_at`

func scanKennelWaiver(sc interface{ Scan(...any) error }) (*KennelWaiver, error) {
	var w KennelWaiver
	var created, expires int64
	if err := sc.Scan(&w.ID, &w.RepositoryPK, &w.Code, &w.Subject, &w.Severity, &w.Reason,
		&w.CreatedBy, &w.CreatedByName, &created, &expires); err != nil {
		return nil, err
	}
	w.CreatedAt, w.ExpiresAt = at(created), at(expires)
	return &w, nil
}

// UpsertKennelWaiver records a waiver. Waiving a finding that already has one
// renews it: the reason, severity, owner and end are replaced, and the ID the
// first waiver had is kept, so a link to it still means something. w.ID and
// w.CreatedAt are filled in.
func (s *Store) UpsertKennelWaiver(ctx context.Context, w *KennelWaiver) error {
	now := s.Now()
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	if !w.ExpiresAt.After(now) {
		return fmt.Errorf("a waiver must end in the future: %w", ErrInvalidKennelEvaluation)
	}
	if _, err := s.exec(ctx, `INSERT INTO kennel_waivers (`+kennelWaiverCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repository_pk, code, subject) DO UPDATE SET
			severity = excluded.severity, reason = excluded.reason, created_by = excluded.created_by,
			created_by_name = excluded.created_by_name, created_at = excluded.created_at,
			expires_at = excluded.expires_at`,
		NewID(PrefixKennelWaiver), w.RepositoryPK, w.Code, w.Subject, w.Severity, w.Reason,
		w.CreatedBy, w.CreatedByName, ms(w.CreatedAt), ms(w.ExpiresAt)); err != nil {
		return wrapWrite(err)
	}
	kept, err := scanKennelWaiver(s.read.QueryRowContext(ctx, `SELECT `+kennelWaiverCols+
		` FROM kennel_waivers WHERE repository_pk = ? AND code = ? AND subject = ?`, w.RepositoryPK, w.Code, w.Subject))
	if err != nil {
		return err
	}
	*w = *kept
	return nil
}

// ListKennelWaivers returns a repository's waivers, those ending last first.
func (s *Store) ListKennelWaivers(ctx context.Context, repositoryPK string) ([]*KennelWaiver, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+kennelWaiverCols+
		` FROM kennel_waivers WHERE repository_pk = ? ORDER BY expires_at DESC, id`, repositoryPK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*KennelWaiver
	for rows.Next() {
		w, err := scanKennelWaiver(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetKennelWaiver returns one waiver.
func (s *Store) GetKennelWaiver(ctx context.Context, id string) (*KennelWaiver, error) {
	w, err := scanKennelWaiver(s.read.QueryRowContext(ctx, `SELECT `+kennelWaiverCols+` FROM kennel_waivers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("kennel waiver %s: %w", id, ErrNotFound)
	}
	return w, err
}

// DeleteKennelWaiver removes one waiver, which is what ending it early is.
func (s *Store) DeleteKennelWaiver(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM kennel_waivers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("kennel waiver %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteKennelWaivers removes waivers the controller has retired because the
// finding they were about stopped being reported.
func (s *Store) DeleteKennelWaivers(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if _, err := s.exec(ctx, `DELETE FROM kennel_waivers WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// CountKennelWaivers is how many waivers a repository has, for the limit on
// them.
func (s *Store) CountKennelWaivers(ctx context.Context, repositoryPK string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM kennel_waivers WHERE repository_pk = ?`, repositoryPK).Scan(&n)
	return n, err
}
