// Package postgres provides a PostgreSQL-backed agent.SessionStore using
// the shared sqlstore engine.
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"

	"github.com/ariloulaleelay/hakka/agent/stores/sqlstore"
)

// Store is a PostgreSQL-backed agent.SessionStore.
type Store struct {
	*sqlstore.Store
	db *sql.DB
}

// Open connects to a PostgreSQL database using the given DSN
// (e.g. "postgres://user:pass@localhost/hakka?sslmode=disable")
// and initialises the schema.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}

	store := &Store{
		Store: sqlstore.New(db, sqlstore.Postgres),
		db:    db,
	}

	ctx := context.Background()
	if err := store.InitSchema(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	// Drop the deprecated streaming column (adapter decides streaming now).
	if err := store.DropStreamingColumn(ctx); err != nil {
		return nil, fmt.Errorf("drop streaming column: %w", err)
	}

	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// IDGenerator creates a sequence-backed ID generator using the store's
// database connection.
func (s *Store) IDGenerator() *sqlstore.SQLIDGenerator {
	return s.Store.NewIDGenerator()
}
