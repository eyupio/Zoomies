package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AIContextUploadHosts are the GitHub hosts with at least one enabled
// Zoomies-only repository: the only hosts whose Actions tokens an upload may
// be checked against.
func (s *Store) AIContextUploadHosts(ctx context.Context) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT DISTINCT github_host FROM ai_context_repositories
		WHERE json_extract(config_json,'$.destination')='zoomies' AND COALESCE(json_extract(config_json,'$.disabled'),0)=0 ORDER BY github_host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

// FindAIContextUploadTarget returns the one Zoomies-only repository with this
// GitHub host and numeric repository ID. An upload names its repository only
// through the token's claims, so anything other than exactly one match --
// none, or the same repository configured under two installations -- is
// refused rather than guessed.
func (s *Store) FindAIContextUploadTarget(ctx context.Context, host string, repositoryID int64) (*AIContextRepository, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+aiContextReadColumns+` FROM ai_context_repositories
		WHERE github_host=? AND repository_id=? AND json_extract(config_json,'$.destination')='zoomies'
		AND COALESCE(json_extract(config_json,'$.disabled'),0)=0 LIMIT 2`, host, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []*AIContextRepository
	for rows.Next() {
		r, err := scanAIContext(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("no single Zoomies-only repository matches this upload: %w", ErrNotFound)
	}
	return found[0], nil
}

// ClaimAIContextUploadToken records an upload token's ID, or returns
// ErrConflict if it has been used before. Expired IDs are pruned in the same
// transaction: once a token has expired its signature check refuses it anyway.
func (s *Store) ClaimAIContextUploadToken(ctx context.Context, tokenID, repositoryID string, expires time.Time) error {
	if tokenID == "" || len(tokenID) > 200 {
		return fmt.Errorf("an upload token needs an ID")
	}
	return wrapWrite(s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ai_context_upload_tokens WHERE expires_at<?`, ms(s.Now())); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO ai_context_upload_tokens VALUES(?,?,?) ON CONFLICT(token_id) DO NOTHING`, tokenID, repositoryID, ms(expires))
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrConflict
		}
		return nil
	}))
}
