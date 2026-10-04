package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
)

// ClaimAIContextMaintenance atomically replaces a settled proposal. Source
// availability closes before publication and never falls back to old context.
func (s *Store) ClaimAIContextMaintenance(ctx context.Context, operation *AIContextSetup, previousHash string, config aicontext.Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if operation.Revision < 2 || len(operation.PlanHash) != 64 || len(operation.PlanJSON) > 4<<20 || len(previousHash) != 64 {
		return fmt.Errorf("invalid reviewed maintenance plan")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	token, now := NewID("ctxlease"), s.Now()
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE ai_context_repositories SET config_json=?,revision=revision+1,available=0,updated_at=? WHERE id=? AND revision=? AND EXISTS(SELECT 1 FROM ai_context_setups WHERE repository_id=? AND plan_hash=? AND lease_until<=?)`, string(encoded), ms(now), operation.RepositoryID, operation.Revision-1, operation.RepositoryID, previousHash, ms(now))
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
		if _, err = tx.ExecContext(ctx, `UPDATE ai_context_setups SET revision=?,plan_hash=?,plan_json=?,state='pending',pr_number=0,pr_url='',lease_token=?,lease_until=?,updated_at=? WHERE repository_id=?`, operation.Revision, operation.PlanHash, operation.PlanJSON, token, ms(now.Add(5*time.Minute)), ms(now), operation.RepositoryID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM ai_context_freshness WHERE repository_id=?`, operation.RepositoryID); err != nil {
			return err
		}
		if config.Disabled {
			for _, table := range []string{"ai_context_connection_repositories", "ai_context_members", "ai_context_snapshots", "ai_context_artifacts"} {
				if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE repository_id=?`, operation.RepositoryID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		operation.LeaseToken = token
		operation.State = "pending"
	}
	return err
}
