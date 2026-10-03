package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
)

const MaxContextStorageBytes = 256 << 20

type AIContextFreshness struct {
	State           string    `json:"state"`
	DesiredCommit   string    `json:"desired_commit"`
	PublishedCommit string    `json:"published_commit"`
	Digest          string    `json:"snapshot_id"`
	CheckedAt       time.Time `json:"checked_at"`
	Failure         string    `json:"failure,omitempty"`
}

func (s *Store) GetAIContextFreshness(ctx context.Context, id string) (*AIContextFreshness, error) {
	var f AIContextFreshness
	var checked int64
	err := s.read.QueryRowContext(ctx, `SELECT state,desired_commit,published_commit,digest,checked_at,failure FROM ai_context_freshness WHERE repository_id=?`, id).Scan(&f.State, &f.DesiredCommit, &f.PublishedCommit, &f.Digest, &checked, &f.Failure)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	f.CheckedAt = at(checked)
	return &f, err
}

// Failed checks close availability before retained source can be read. The last
// successful snapshot stays intact and never masquerades as today's source.
func (s *Store) FailAIContextCheck(ctx context.Context, id, state, desired, reason string) error {
	if state != "awaiting_merge" && state != "unavailable" && state != "stale" {
		return fmt.Errorf("invalid context verification state")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE ai_context_repositories SET available=0 WHERE id=?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ai_context_freshness(repository_id,state,desired_commit,checked_at,failure) VALUES(?,?,?,?,?) ON CONFLICT(repository_id) DO UPDATE SET state=excluded.state,desired_commit=excluded.desired_commit,checked_at=excluded.checked_at,failure=excluded.failure`, id, state, desired, ms(s.Now()), reason)
		return err
	})
}
func (s *Store) PublishAIContextSnapshot(ctx context.Context, id string, revision int64, snapshot *aicontext.Snapshot) error {
	return s.publishAIContextSnapshot(ctx, id, revision, snapshot, MaxContextStorageBytes)
}

func (s *Store) publishAIContextSnapshot(ctx context.Context, id string, revision int64, snapshot *aicontext.Snapshot, storageLimit int64) error {
	if storageLimit < 1 || storageLimit > MaxContextStorageBytes {
		return fmt.Errorf("invalid context storage limit")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(body) > aicontext.MaxSnapshotBytes {
		return fmt.Errorf("context exceeds the encoded snapshot limit")
	}
	digest := aicontext.Hash(body)
	return s.tx(ctx, func(tx *sql.Tx) error {
		var configJSON, host, installation string
		var numericID, currentRevision int64
		if err := tx.QueryRowContext(ctx, `SELECT config_json,github_host,installation_id,repository_id,revision FROM ai_context_repositories WHERE id=?`, id).Scan(&configJSON, &host, &installation, &numericID, &currentRevision); err != nil {
			return err
		}
		if currentRevision != revision {
			return ErrConflict
		}
		var config aicontext.Config
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return err
		}
		hash, err := config.Hash()
		if err != nil {
			return err
		}
		if err := config.CheckSourceFiles(snapshot.Files); err != nil {
			return err
		}
		if config.Disabled {
			return fmt.Errorf("removed context cannot receive snapshots")
		}
		if config.Destination != aicontext.Both {
			return fmt.Errorf("durable context snapshots require repository and Zoomies output")
		}
		if err := snapshot.Match(aicontext.RepositoryKey{GitHubHost: host, InstallationID: installation, RepositoryID: numericID}, config.SourceBranch, snapshot.Manifest.SourceCommit, hash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_context_snapshots VALUES(?,?,?,?,?,?) ON CONFLICT(repository_id,digest) DO UPDATE SET created_at=excluded.created_at`, id, digest, snapshot.Manifest.SourceCommit, hash, body, ms(s.Now())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ai_context_snapshots WHERE repository_id=? AND digest!=? AND digest NOT IN (SELECT digest FROM ai_context_snapshots WHERE repository_id=? AND digest!=? ORDER BY created_at DESC,digest LIMIT ?)`, id, digest, id, digest, config.KeepSnapshots-1); err != nil {
			return err
		}
		var total int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(body)),0) FROM ai_context_snapshots`).Scan(&total); err != nil {
			return err
		}
		if total > storageLimit {
			return fmt.Errorf("context storage is full; reduce retained snapshots before retrying")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_context_freshness VALUES(?,'ready',?,?,?,?, '') ON CONFLICT(repository_id) DO UPDATE SET state='ready',desired_commit=excluded.desired_commit,published_commit=excluded.published_commit,digest=excluded.digest,checked_at=excluded.checked_at,failure=''`, id, snapshot.Manifest.SourceCommit, snapshot.Manifest.SourceCommit, digest, ms(s.Now())); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE ai_context_repositories SET available=1,updated_at=? WHERE id=?`, ms(s.Now()), id)
		return err
	})
}
func (s *Store) GetAIContextSnapshot(ctx context.Context, id, digest string) (*aicontext.Snapshot, error) {
	var body []byte
	err := s.read.QueryRowContext(ctx, `SELECT body FROM ai_context_snapshots WHERE repository_id=? AND digest=? AND length(body)<=?`, id, digest, aicontext.MaxSnapshotBytes).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(body) > aicontext.MaxSnapshotBytes || aicontext.Hash(body) != digest {
		return nil, fmt.Errorf("stored context fails integrity verification")
	}
	return aicontext.Decode(bytes.NewReader(body))
}

// Only explicitly submitted proposals enter background verification.
func (s *Store) ListAIContextVerificationCandidates(ctx context.Context, limit, offset int) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT r.id FROM ai_context_repositories r JOIN ai_context_setups s ON s.repository_id=r.id WHERE s.state='awaiting_merge' ORDER BY r.id LIMIT ? OFFSET ?`, min(max(limit, 1), 20), max(offset, 0))
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

// A successful live recheck changes freshness without rewriting retained source.
func (s *Store) ConfirmAIContextSnapshot(ctx context.Context, id string, revision int64, digest, commit, configHash string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE ai_context_repositories SET available=1,updated_at=? WHERE id=? AND revision=? AND EXISTS(SELECT 1 FROM ai_context_snapshots WHERE repository_id=? AND digest=? AND source_commit=? AND config_hash=?)`, ms(s.Now()), id, revision, id, digest, commit, configHash)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ai_context_freshness VALUES(?,'ready',?,?,?,?, '') ON CONFLICT(repository_id) DO UPDATE SET state='ready',desired_commit=excluded.desired_commit,published_commit=excluded.published_commit,digest=excluded.digest,checked_at=excluded.checked_at,failure=''`, id, commit, commit, digest, ms(s.Now()))
		return err
	})
}

// Repository-only output keeps its source in the repository. A verified check
// still has to mark the repository available and record what was seen, but the
// store holds no payload: the digest names a snapshot that exists only for the
// request that read it.
func (s *Store) ConfirmAIContextTransient(ctx context.Context, id string, revision int64, commit, digest, configHash string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var configJSON string
		if err := tx.QueryRowContext(ctx, `SELECT config_json FROM ai_context_repositories WHERE id=? AND revision=?`, id, revision).Scan(&configJSON); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		var config aicontext.Config
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return err
		}
		if hash, err := config.Hash(); err != nil || hash != configHash {
			return ErrConflict
		}
		if config.Disabled || config.Destination != aicontext.Repository {
			return fmt.Errorf("transient context requires repository-only output")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_context_repositories SET available=1,updated_at=? WHERE id=?`, ms(s.Now()), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ai_context_freshness VALUES(?,'ready',?,?,?,?, '') ON CONFLICT(repository_id) DO UPDATE SET state='ready',desired_commit=excluded.desired_commit,published_commit=excluded.published_commit,digest=excluded.digest,checked_at=excluded.checked_at,failure=''`, id, commit, commit, digest, ms(s.Now()))
		return err
	})
}
