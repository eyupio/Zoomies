package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type AIContextSetup struct {
	RepositoryID string    `json:"repository_id"`
	Revision     int64     `json:"revision"`
	PlanHash     string    `json:"plan_hash"`
	PlanJSON     string    `json:"-"`
	State        string    `json:"state"`
	PRNumber     int       `json:"pr_number"`
	PRURL        string    `json:"pr_url"`
	LeaseToken   string    `json:"-"`
	LeaseUntil   time.Time `json:"-"`
}

func (s *Store) GetAIContextSetup(ctx context.Context, id string) (*AIContextSetup, error) {
	var r AIContextSetup
	var lease int64
	err := s.read.QueryRowContext(ctx, `SELECT repository_id,revision,plan_hash,plan_json,state,pr_number,pr_url,lease_token,lease_until FROM ai_context_setups WHERE repository_id=?`, id).Scan(&r.RepositoryID, &r.Revision, &r.PlanHash, &r.PlanJSON, &r.State, &r.PRNumber, &r.PRURL, &r.LeaseToken, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.LeaseUntil = at(lease)
	return &r, err
}

// ClaimAIContextSetup freezes the reviewed configuration before any write.
// The lease serialises calls across restarts; retries retain the same plan.
func (s *Store) ClaimAIContextSetup(ctx context.Context, r *AIContextSetup) error {
	if len(r.PlanJSON) > 4<<20 || len(r.PlanHash) != 64 || r.Revision < 1 {
		return fmt.Errorf("invalid context setup plan")
	}
	token := NewID("ctxlease")
	now := s.Now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM ai_context_repositories WHERE id=?`, r.RepositoryID).Scan(&revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if revision != r.Revision {
			return ErrConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ai_context_setups(repository_id,revision,plan_hash,plan_json,state,updated_at) VALUES(?,?,?,?,'pending',?) ON CONFLICT(repository_id) DO NOTHING`, r.RepositoryID, r.Revision, r.PlanHash, r.PlanJSON, ms(now))
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE ai_context_setups SET lease_token=?,lease_until=?,updated_at=? WHERE repository_id=? AND revision=? AND plan_hash=? AND state='pending' AND lease_until<=?`, token, ms(now.Add(5*time.Minute)), ms(now), r.RepositoryID, r.Revision, r.PlanHash, ms(now))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
	if err == nil {
		r.LeaseToken = token
		r.State = "pending"
	}
	return err
}

func (s *Store) ReleaseAIContextSetup(ctx context.Context, id, token string) error {
	_, err := s.exec(ctx, `UPDATE ai_context_setups SET lease_token='',lease_until=0 WHERE repository_id=? AND lease_token=?`, id, token)
	return err
}

func (s *Store) CompleteAIContextSetup(ctx context.Context, id, token string, number int, url string) error {
	if number <= 0 || url == "" {
		return fmt.Errorf("setup needs the reviewed pull request identity")
	}
	res, err := s.exec(ctx, `UPDATE ai_context_setups SET state='awaiting_merge',pr_number=?,pr_url=?,lease_token='',lease_until=0,updated_at=? WHERE repository_id=? AND lease_token=? AND state='pending'`, number, url, ms(s.Now()), id, token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
