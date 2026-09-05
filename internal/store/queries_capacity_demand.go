package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) GetCapacityDemandDelivery(ctx context.Context, poolID, eventType string) (*CapacityDemandDelivery, error) {
	var d CapacityDemandDelivery
	var observed int64
	var attempted, delivered sql.NullInt64
	err := s.read.QueryRowContext(ctx, `SELECT pool_id,event_type,event_id,payload,observed_since,attempted_at,delivered_at,status_code,attempts,last_error FROM capacity_demand_deliveries WHERE pool_id=? AND event_type=?`, poolID, eventType).Scan(&d.PoolID, &d.EventType, &d.EventID, &d.Payload, &observed, &attempted, &delivered, &d.StatusCode, &d.Attempts, &d.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.ObservedSince = at(observed)
	if attempted.Valid {
		t := at(attempted.Int64)
		d.AttemptedAt = &t
	}
	if delivered.Valid {
		t := at(delivered.Int64)
		d.DeliveredAt = &t
	}
	return &d, nil
}

func (s *Store) PutCapacityDemandDelivery(ctx context.Context, d *CapacityDemandDelivery) error {
	_, err := s.exec(ctx, `INSERT INTO capacity_demand_deliveries(pool_id,event_type,event_id,payload,observed_since,attempted_at,delivered_at,status_code,attempts,last_error) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(pool_id,event_type) DO UPDATE SET event_id=excluded.event_id,payload=excluded.payload,observed_since=excluded.observed_since,attempted_at=excluded.attempted_at,delivered_at=excluded.delivered_at,status_code=excluded.status_code,attempts=excluded.attempts,last_error=excluded.last_error`, d.PoolID, d.EventType, d.EventID, d.Payload, ms(d.ObservedSince), msp(d.AttemptedAt), msp(d.DeliveredAt), d.StatusCode, d.Attempts, d.LastError)
	if err != nil {
		return fmt.Errorf("saving capacity-demand delivery: %w", err)
	}
	return nil
}

func (s *Store) ListCapacityDemandDeliveries(ctx context.Context) ([]*CapacityDemandDelivery, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT pool_id,event_type,event_id,payload,observed_since,attempted_at,delivered_at,status_code,attempts,last_error FROM capacity_demand_deliveries WHERE attempted_at IS NOT NULL ORDER BY attempted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CapacityDemandDelivery
	for rows.Next() {
		var d CapacityDemandDelivery
		var o int64
		var a, z sql.NullInt64
		if err := rows.Scan(&d.PoolID, &d.EventType, &d.EventID, &d.Payload, &o, &a, &z, &d.StatusCode, &d.Attempts, &d.LastError); err != nil {
			return nil, err
		}
		d.ObservedSince = at(o)
		if a.Valid {
			t := at(a.Int64)
			d.AttemptedAt = &t
		}
		if z.Valid {
			t := at(z.Int64)
			d.DeliveredAt = &t
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCapacityDemandObservation(ctx context.Context, poolID, eventType string) error {
	_, err := s.exec(ctx, `DELETE FROM capacity_demand_deliveries WHERE pool_id=? AND event_type=? AND attempted_at IS NULL`, poolID, eventType)
	return err
}
