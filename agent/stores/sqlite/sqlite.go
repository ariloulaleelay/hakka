// Package sqlite provides a SQLite-backed agent.SessionStore using
// the shared sqlstore engine. It also handles migration from the
// pre-split schema (messages as JSON blob in sessions table).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/ariloulaleelay/hakka/agent/stores/sqlstore"
)

// Store is a SQLite-backed agent.SessionStore.
type Store struct {
	*sqlstore.Store
	db *sql.DB
}

// Open opens (or creates) a SQLite database at the given path and
// initialises the schema. If the database uses the old pre-split
// schema (messages as JSON column in sessions), it is migrated
// automatically.
func Open(path string) (*Store, error) {
	dsn := path
	pragma := "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	if strings.Contains(path, "?") {
		dsn = path + "&" + pragma
	} else {
		dsn = path + "?" + pragma
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}

	store := &Store{
		Store: sqlstore.New(db, sqlstore.SQLite),
		db:    db,
	}

	ctx := context.Background()

	// First, check if the sessions table already exists (old schema).
	// We need to handle this before InitSchema because the old sessions
	// table has a "messages" column that conflicts with our new schema
	// expectations. InitSchema creates the new sessions table if it
	// doesn't exist, but if the OLD table exists, CREATE IF NOT EXISTS
	// does nothing and we still have the messages column.

	needsMigration, err := store.NeedsMigration(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("check migration: %w", err)
	}

	// Create tables (messages table is new; sessions IF NOT EXISTS won't
	// touch an existing old-schema table).
	if err := store.InitSchema(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	if needsMigration {
		if err := store.MigrateFromOldSchema(ctx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("migrate old schema: %w", err)
		}
	}

	// Drop the deprecated streaming column (adapter decides streaming now).
	if err := store.DropStreamingColumn(ctx); err != nil {
		slog.Warn("sqlite: failed to drop streaming column", "error", err)
	}

	return store, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
