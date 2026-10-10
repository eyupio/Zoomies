package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type EliIdentity struct {
	UserID       string `json:"user_id"`
	GitHubUserID int64  `json:"github_user_id"`
	GitHubLogin  string `json:"github_login"`
	Confirmed    bool   `json:"confirmed"`
}
type EliRepairPolicy struct {
	Repo           string `json:"repo"`
	InstallationID string `json:"installation_id"`
	ProviderID     string `json:"provider_id"`
	Enabled        bool   `json:"enabled"`
	Automatic      bool   `json:"automatic"`
	AllowWorkflows bool   `json:"allow_workflows"`
	DailyLimit     int    `json:"daily_limit"`
}
type EliRepair struct {
	ID             string    `json:"id"`
	DedupKey       string    `json:"-"`
	InstallationID string    `json:"installation_id"`
	Repo           string    `json:"repo"`
	PullNumber     int       `json:"pull_number"`
	JobID          int64     `json:"job_id"`
	RunID          int64     `json:"run_id"`
	HeadSHA        string    `json:"head_sha"`
	CommitSHA      string    `json:"commit_sha"`
	UserID         string    `json:"user_id"`
	GitHubUserID   int64     `json:"github_user_id"`
	GitHubLogin    string    `json:"github_login"`
	ProviderID     string    `json:"provider_id"`
	Trigger        string    `json:"trigger"`
	Instruction    string    `json:"-"`
	State          string    `json:"state"`
	Message        string    `json:"message"`
	CommentID      int64     `json:"comment_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *Store) SetEliIdentity(ctx context.Context, in EliIdentity) error {
	_, err := s.exec(ctx, `INSERT INTO eli_github_identities(user_id,github_user_id,github_login,updated_at) VALUES(?,?,?,?)
 ON CONFLICT(user_id) DO UPDATE SET github_user_id=excluded.github_user_id,github_login=excluded.github_login,confirmed=0,updated_at=excluded.updated_at`, in.UserID, in.GitHubUserID, in.GitHubLogin, ms(s.Now()))
	return wrapWrite(err)
}
func (s *Store) EliIdentities(ctx context.Context) ([]EliIdentity, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT user_id,github_user_id,github_login,confirmed FROM eli_github_identities ORDER BY github_login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EliIdentity{}
	for rows.Next() {
		var v EliIdentity
		if err := rows.Scan(&v.UserID, &v.GitHubUserID, &v.GitHubLogin, &v.Confirmed); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) DeleteEliIdentity(ctx context.Context, userID string) error {
	_, err := s.exec(ctx, `DELETE FROM eli_github_identities WHERE user_id=?`, userID)
	return err
}
func (s *Store) SetEliRepairPolicy(ctx context.Context, p EliRepairPolicy) error {
	if p.DailyLimit < 1 || p.DailyLimit > 50 {
		return ErrConflict
	}
	_, err := s.exec(ctx, `INSERT INTO eli_repair_policies(repo,installation_id,provider_id,enabled,automatic,allow_workflows,daily_limit,updated_at) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(repo) DO UPDATE SET installation_id=excluded.installation_id,provider_id=excluded.provider_id,enabled=excluded.enabled,automatic=excluded.automatic,allow_workflows=excluded.allow_workflows,daily_limit=excluded.daily_limit,updated_at=excluded.updated_at`, p.Repo, p.InstallationID, p.ProviderID, boolInt(p.Enabled), boolInt(p.Automatic), boolInt(p.AllowWorkflows), p.DailyLimit, ms(s.Now()))
	return wrapWrite(err)
}
func (s *Store) EliRepairPolicies(ctx context.Context) ([]EliRepairPolicy, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT repo,installation_id,provider_id,enabled,automatic,allow_workflows,daily_limit FROM eli_repair_policies ORDER BY repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EliRepairPolicy{}
	for rows.Next() {
		var p EliRepairPolicy
		if err := rows.Scan(&p.Repo, &p.InstallationID, &p.ProviderID, &p.Enabled, &p.Automatic, &p.AllowWorkflows, &p.DailyLimit); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const eliRepairCols = `id,dedup_key,installation_id,repo,pull_number,job_id,run_id,head_sha,commit_sha,user_id,github_user_id,github_login,provider_id,trigger,instruction,state,message,comment_id,created_at,updated_at`

func scanEliRepair(row interface{ Scan(...any) error }) (*EliRepair, error) {
	var v EliRepair
	var created, updated int64
	err := row.Scan(&v.ID, &v.DedupKey, &v.InstallationID, &v.Repo, &v.PullNumber, &v.JobID, &v.RunID, &v.HeadSHA, &v.CommitSHA, &v.UserID, &v.GitHubUserID, &v.GitHubLogin, &v.ProviderID, &v.Trigger, &v.Instruction, &v.State, &v.Message, &v.CommentID, &created, &updated)
	v.CreatedAt, v.UpdatedAt = at(created), at(updated)
	return &v, err
}

// Admission and deduplication share the writer transaction, so a webhook burst cannot outrun the budget.
func (s *Store) EnqueueEliRepair(ctx context.Context, r *EliRepair, limit int) (bool, error) {
	added := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT id FROM eli_repairs WHERE dedup_key=?`, r.DedupKey).Scan(&existing)
		if err == nil {
			saved, err := scanEliRepair(tx.QueryRowContext(ctx, `SELECT `+eliRepairCols+` FROM eli_repairs WHERE id=?`, existing))
			if err != nil {
				return err
			}
			*r = *saved
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM eli_repairs WHERE repo=? AND created_at>=?`, r.Repo, ms(s.Now().Add(-24*time.Hour))).Scan(&count); err != nil {
			return err
		}
		if limit < 1 || count >= limit {
			return ErrConflict
		}
		if r.UserID != "" {
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM eli_repairs WHERE user_id=? AND created_at>=?`, r.UserID, ms(s.Now().Add(-24*time.Hour))).Scan(&count); err != nil {
				return err
			}
			if count >= 10 {
				return ErrConflict
			}
		}
		r.ID = NewID(PrefixEliRepair)
		r.State = "queued"
		r.CreatedAt = s.Now()
		r.UpdatedAt = r.CreatedAt
		_, err = tx.ExecContext(ctx, `INSERT INTO eli_repairs (`+eliRepairCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.DedupKey, r.InstallationID, r.Repo, r.PullNumber, r.JobID, r.RunID, r.HeadSHA, r.CommitSHA, r.UserID, r.GitHubUserID, r.GitHubLogin, r.ProviderID, r.Trigger, r.Instruction, r.State, r.Message, r.CommentID, ms(r.CreatedAt), ms(r.UpdatedAt))
		added = err == nil
		return err
	})
	return added, wrapWrite(err)
}
func (s *Store) ListEliRepairs(ctx context.Context) ([]*EliRepair, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+eliRepairCols+` FROM eli_repairs ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EliRepair{}
	for rows.Next() {
		r, err := scanEliRepair(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) NextEliRepair(ctx context.Context) (*EliRepair, error) {
	r, err := scanEliRepair(s.read.QueryRowContext(ctx, `SELECT `+eliRepairCols+` FROM eli_repairs WHERE state='queued' ORDER BY created_at LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}
func (s *Store) SaveEliRepair(ctx context.Context, r *EliRepair) error {
	r.UpdatedAt = s.Now()
	_, err := s.exec(ctx, `UPDATE eli_repairs SET pull_number=?,head_sha=?,commit_sha=?,user_id=?,provider_id=?,state=?,message=?,comment_id=?,updated_at=? WHERE id=?`, r.PullNumber, r.HeadSHA, r.CommitSHA, r.UserID, r.ProviderID, r.State, r.Message, r.CommentID, ms(r.UpdatedAt), r.ID)
	return err
}

// An interrupted write has an uncertain outcome. Never automatically repeat it after restart.
func (s *Store) InterruptEliRepairs(ctx context.Context) error {
	_, err := s.exec(ctx, `UPDATE eli_repairs SET state='interrupted',message='The controller restarted during this repair. Check the PR before asking again.',updated_at=? WHERE state='working'`, ms(s.Now()))
	return err
}

// Repair commits remain protected from automatic recursion beyond the history page's limit.
func (s *Store) IsEliRepairCommit(ctx context.Context, repo, sha string) (bool, error) {
	var count int
	err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM eli_repairs WHERE repo=? AND commit_sha=?`, repo, sha).Scan(&count)
	return count > 0, err
}
func (s *Store) CheckingEliRepairs(ctx context.Context) ([]*EliRepair, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+eliRepairCols+` FROM eli_repairs WHERE state='checking' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EliRepair{}
	for rows.Next() {
		r, err := scanEliRepair(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Only the owner can approve a verified link. Pinning the numeric ID prevents a stale
// confirmation screen from approving a different account linked in the meantime.
func (s *Store) ConfirmEliIdentity(ctx context.Context, userID string, githubID int64, enabled bool) error {
	result, err := s.exec(ctx, `UPDATE eli_github_identities SET confirmed=?,updated_at=? WHERE user_id=? AND github_user_id=?`, boolInt(enabled), ms(s.Now()), userID, githubID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
