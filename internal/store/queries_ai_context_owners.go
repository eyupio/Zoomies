package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MaxContextInstallationOwners bounds one installation's owner list so an
// administrator's replace call stays a single small transaction.
const MaxContextInstallationOwners = 50

// AIContextInstallationOwners lists the users who may enable AI Context for an
// installation, in a stable order.
func (s *Store) AIContextInstallationOwners(ctx context.Context, installationID string) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT user_id FROM ai_context_installation_owners WHERE installation_id=? ORDER BY user_id`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReplaceAIContextInstallationOwners makes the owner list exactly users. Unknown
// installations and users are refused rather than skipped, so a typo cannot look
// like a successful delegation.
func (s *Store) ReplaceAIContextInstallationOwners(ctx context.Context, installationID string, users []string) error {
	if len(users) > MaxContextInstallationOwners {
		return fmt.Errorf("select at most %d installation owners", MaxContextInstallationOwners)
	}
	return wrapWrite(s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM installations WHERE id=?`, installationID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		for _, user := range users {
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id=? AND disabled=0`, user).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: choose existing, enabled users as installation owners", ErrNotFound)
			} else if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ai_context_installation_owners WHERE installation_id=?`, installationID); err != nil {
			return err
		}
		for _, user := range users {
			if _, err := tx.ExecContext(ctx, `INSERT INTO ai_context_installation_owners VALUES(?,?,?)`, installationID, user, ms(s.Now())); err != nil {
				return err
			}
		}
		return nil
	}))
}

// UserOwnsAIContextInstallation checks live state: a disabled user owns nothing.
func (s *Store) UserOwnsAIContextInstallation(ctx context.Context, installationID, userID string) (bool, error) {
	var owns int
	err := s.read.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_context_installation_owners o JOIN users u ON u.id=o.user_id
		WHERE o.installation_id=? AND o.user_id=? AND u.disabled=0)`, installationID, userID).Scan(&owns)
	return owns == 1, err
}

// AIContextInstallationRef is the installation set a user owns, with
// just enough to label it. It is the whole of what an owner learns about the
// fleet's installations.
type AIContextInstallationRef struct {
	ID         string `json:"id"`
	Target     string `json:"target"`
	TargetType string `json:"target_type"`
}

func (s *Store) AIContextOwnedInstallations(ctx context.Context, userID string) ([]AIContextInstallationRef, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT i.id,i.target,i.target_type FROM installations i
		JOIN ai_context_installation_owners o ON o.installation_id=i.id JOIN users u ON u.id=o.user_id
		WHERE o.user_id=? AND u.disabled=0 ORDER BY i.target,i.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIContextInstallationRef{}
	for rows.Next() {
		var ref AIContextInstallationRef
		if err := rows.Scan(&ref.ID, &ref.Target, &ref.TargetType); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// AllAIContextInstallations lists every installation for an administrator's
// picker, in the same minimal shape owners see.
func (s *Store) AllAIContextInstallations(ctx context.Context) ([]AIContextInstallationRef, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id,target,target_type FROM installations ORDER BY target,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIContextInstallationRef{}
	for rows.Next() {
		var ref AIContextInstallationRef
		if err := rows.Scan(&ref.ID, &ref.Target, &ref.TargetType); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
