package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// The blank import registers the "sqlite" driver with database/sql.
	// This driver is pure Go, so the program still cross-compiles without
	// a C toolchain.
	_ "modernc.org/sqlite"
)

// Store keeps raw samples in SQLite. Usage is always derived from them at
// query time, so a change to the delta logic never needs a data migration.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS samples (
	ts             INTEGER PRIMARY KEY, -- Unix seconds, UTC
	energy_wh      REAL    NOT NULL,
	temperature_c  REAL    NOT NULL,
	network_rx_kib INTEGER NOT NULL,
	network_tx_kib INTEGER NOT NULL
) WITHOUT ROWID;`

func openStore(ctx context.Context, path string) (*Store, error) {
	// WAL lets the dashboard read while the collector writes; busy_timeout
	// makes a writer wait for a lock instead of failing immediately.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Insert(ctx context.Context, sample Sample) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO samples VALUES (?, ?, ?, ?, ?)`,
		sample.Time.Unix(), sample.EnergyWh, sample.TemperatureC,
		sample.NetworkRxKiB, sample.NetworkTxKiB)
	return err
}

const sampleColumns = `ts, energy_wh, temperature_c, network_rx_kib, network_tx_kib`

// Range returns the samples in [from, to) in time order, preceded by the
// last sample before from if there is one. That extra sample is the baseline
// the first in-range sample's usage is measured against.
func (s *Store) Range(ctx context.Context, from, to time.Time) ([]Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+sampleColumns+` FROM samples
		WHERE ts >= COALESCE((SELECT MAX(ts) FROM samples WHERE ts < ?1), ?1)
		  AND ts < ?2
		ORDER BY ts`,
		from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples []Sample
	for rows.Next() {
		sample, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	// rows.Next returns false both at the end and on failure; Err tells
	// the two apart.
	return samples, rows.Err()
}

// Latest returns the most recent sample. ok is false if there are none yet.
func (s *Store) Latest(ctx context.Context) (sample Sample, ok bool, err error) {
	return s.one(ctx, `SELECT `+sampleColumns+` FROM samples ORDER BY ts DESC LIMIT 1`)
}

// Earliest returns the oldest sample. ok is false if there are none yet.
func (s *Store) Earliest(ctx context.Context) (sample Sample, ok bool, err error) {
	return s.one(ctx, `SELECT `+sampleColumns+` FROM samples ORDER BY ts LIMIT 1`)
}

func (s *Store) one(ctx context.Context, query string) (Sample, bool, error) {
	sample, err := scanSample(s.db.QueryRowContext(ctx, query))
	if errors.Is(err, sql.ErrNoRows) {
		return Sample{}, false, nil
	}
	return sample, err == nil, err
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanSample(row scanner) (Sample, error) {
	var (
		sample Sample
		ts     int64
	)
	err := row.Scan(&ts, &sample.EnergyWh, &sample.TemperatureC,
		&sample.NetworkRxKiB, &sample.NetworkTxKiB)
	sample.Time = time.Unix(ts, 0).UTC()
	return sample, err
}
