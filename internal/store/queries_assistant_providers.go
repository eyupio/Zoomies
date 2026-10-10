package store

import (
	"context"
	"database/sql"
	"time"
)

// AssistantProvider is one model the assistant may talk to. KeyEnc is the
// API key sealed with the instance key and is never rendered; LastCheck is
// the JSON the last check returned, which the controller writes and reads.
type AssistantProvider struct {
	OwnerID       string
	ID            string
	Name          string
	Kind          string
	BaseURL       string
	Model         string
	KeyEnc        []byte
	Enabled       bool
	IsDefault     bool
	LastCheck     []byte
	LastCheckedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const assistantProviderCols = `id, owner_id, name, kind, base_url, model, key_enc, enabled, is_default,
	last_check, last_checked_at, created_at, updated_at`

func scanAssistantProvider(sc interface{ Scan(...any) error }) (*AssistantProvider, error) {
	var p AssistantProvider
	var enabled, isDefault int
	var created, updated int64
	var check sql.NullString
	var checked sql.NullInt64
	if err := sc.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Kind, &p.BaseURL, &p.Model, &p.KeyEnc, &enabled, &isDefault,
		&check, &checked, &created, &updated); err != nil {
		return nil, err
	}
	p.Enabled, p.IsDefault = enabled == 1, isDefault == 1
	if check.Valid {
		p.LastCheck = []byte(check.String)
	}
	p.LastCheckedAt = atp(checked)
	p.CreatedAt, p.UpdatedAt = at(created), at(updated)
	return &p, nil
}

// ListAssistantProviders returns every provider, by name.
func (s *Store) ListAssistantProviders(ctx context.Context) ([]*AssistantProvider, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+assistantProviderCols+` FROM assistant_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AssistantProvider
	for rows.Next() {
		p, err := scanAssistantProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetAssistantProvider returns one provider, or ErrNotFound.
func (s *Store) GetAssistantProvider(ctx context.Context, id string) (*AssistantProvider, error) {
	p, err := scanAssistantProvider(s.read.QueryRowContext(ctx,
		`SELECT `+assistantProviderCols+` FROM assistant_providers WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return p, err
}

// CreateAssistantProvider inserts a provider. The key is not part of it:
// SetAssistantProviderKey writes the sealed bytes, so the one place that has
// to think about the instance key is the one place that holds it.
func (s *Store) CreateAssistantProvider(ctx context.Context, p *AssistantProvider) error {
	if p.ID == "" {
		p.ID = NewID(PrefixAssistantProvider)
	}
	now := s.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := s.exec(ctx, `INSERT INTO assistant_providers
		(id, owner_id, name, kind, base_url, model, enabled, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		p.ID, p.OwnerID, p.Name, p.Kind, p.BaseURL, p.Model, boolInt(p.Enabled), ms(now), ms(now))
	return wrapWrite(err)
}

// UpdateAssistantProvider persists the operator's half of a row: name, kind,
// address, model and whether it is enabled. The key and the default have
// their own writers, so a form submitted from yesterday's page carries
// neither.
func (s *Store) UpdateAssistantProvider(ctx context.Context, p *AssistantProvider) error {
	now := s.Now()
	res, err := s.exec(ctx, `UPDATE assistant_providers
		SET name=?, kind=?, base_url=?, model=?, enabled=?, updated_at=? WHERE id=?`,
		p.Name, p.Kind, p.BaseURL, p.Model, boolInt(p.Enabled), ms(now), p.ID)
	if err != nil {
		return wrapWrite(err)
	}
	p.UpdatedAt = now
	return affected(res, "assistant provider", p.ID)
}

// SetAssistantProviderKey replaces the sealed key.
func (s *Store) SetAssistantProviderKey(ctx context.Context, id string, enc []byte) error {
	res, err := s.exec(ctx, `UPDATE assistant_providers SET key_enc=?, updated_at=? WHERE id=?`, enc, ms(s.Now()), id)
	if err != nil {
		return err
	}
	return affected(res, "assistant provider", id)
}

// SetDefaultAssistantProvider makes one provider the default and no other,
// in one transaction, so no reader ever sees two or none in between.
func (s *Store) SetDefaultAssistantProvider(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE assistant_providers SET is_default=0 WHERE is_default=1 AND owner_id=(SELECT owner_id FROM assistant_providers WHERE id=?) AND id<>?`, id, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE assistant_providers SET is_default=1 WHERE id=?`, id)
		if err != nil {
			return err
		}
		return affected(res, "assistant provider", id)
	})
}

// SetAssistantProviderCheck records what the last check returned, leaving
// updated_at alone: a check is an observation, not a change.
func (s *Store) SetAssistantProviderCheck(ctx context.Context, id string, result []byte, checkedAt time.Time) error {
	res, err := s.exec(ctx, `UPDATE assistant_providers SET last_check=?, last_checked_at=? WHERE id=?`,
		string(result), ms(checkedAt), id)
	if err != nil {
		return err
	}
	return affected(res, "assistant provider", id)
}

// DeleteAssistantProvider removes a provider. Deleting the default leaves no
// default rather than promoting another: which model answers is a choice an
// administrator makes, not one the database makes for them.
func (s *Store) DeleteAssistantProvider(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM assistant_providers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "assistant provider", id)
}
