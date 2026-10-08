package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ProviderSetup holds a short-lived enrolment capability and a sealed connection
// draft. It never contains a runner join token or changes an existing host.
type ProviderSetup struct {
	ID         string
	TokenHash  []byte `json:"-"`
	ExpiresAt  time.Time
	PayloadEnc []byte `json:"-"`
}

func (s *Store) CreateProviderSetup(ctx context.Context, p *ProviderSetup) error {
	// Expired credentials are removed as new setup commands are issued.
	if _, err := s.exec(ctx, `DELETE FROM provider_setups WHERE expires_at <= ?`, ms(s.Now())); err != nil {
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO provider_setups (id, token_hash, expires_at) VALUES (?, ?, ?)`, p.ID, p.TokenHash, ms(p.ExpiresAt))
	return wrapWrite(err)
}

func (s *Store) GetProviderSetup(ctx context.Context, id string) (*ProviderSetup, error) {
	p := &ProviderSetup{}
	var expires int64
	err := s.read.QueryRowContext(ctx, `SELECT id, token_hash, expires_at, payload_enc FROM provider_setups WHERE id = ? AND expires_at > ?`, id, ms(s.Now())).Scan(&p.ID, &p.TokenHash, &expires, &p.PayloadEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	p.ExpiresAt = at(expires)
	return p, err
}

// CompleteProviderSetup consumes the capability once, atomically, even when two
// enrolment commands arrive together. Only the controller can open the payload.
func (s *Store) CompleteProviderSetup(ctx context.Context, id string, hash, sealed []byte) error {
	res, err := s.exec(ctx, `UPDATE provider_setups SET payload_enc = ? WHERE id = ? AND token_hash = ? AND expires_at > ? AND payload_enc IS NULL`, sealed, id, hash, ms(s.Now()))
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

func (s *Store) DeleteProviderSetup(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM provider_setups WHERE id = ?`, id)
	return err
}
