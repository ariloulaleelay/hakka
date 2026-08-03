package sqlstore

import (
	"database/sql"
	"fmt"

	"github.com/ariloulaleelay/hakka/agent"
)

// SQLIDGenerator produces monotonic base62 IDs backed by a database
// sequence table. Each call to GenerateID increments the sequence and
// encodes the new value as a base62 string.
type SQLIDGenerator struct {
	db      *sql.DB
	dialect Dialect
}

// NewSQLIDGenerator creates a SQLIDGenerator. The id_sequences table
// must already exist (created by InitSchema).
func NewSQLIDGenerator(db *sql.DB, dialect Dialect) *SQLIDGenerator {
	return &SQLIDGenerator{db: db, dialect: dialect}
}

// GenerateID increments the global sequence and returns the base62
// encoding of the new value. Returns "0" if the sequence hasn't been
// seeded yet.
func (g *SQLIDGenerator) GenerateID() string {
	var nextVal int64
	switch g.dialect {
	case Postgres:
		err := g.db.QueryRow(
			`UPDATE id_sequences SET next_val = next_val + 1 WHERE name = 'global' RETURNING next_val`,
		).Scan(&nextVal)
		if err != nil {
			// If no row exists yet (table just created), return "0".
			return "0"
		}
	default:
		// SQLite: UPDATE ... RETURNING not supported before 3.35.
		// Use a transaction with two queries.
		tx, err := g.db.Begin()
		if err != nil {
			return "0"
		}
		defer tx.Rollback()

		_, err = tx.Exec(`UPDATE id_sequences SET next_val = next_val + 1 WHERE name = 'global'`)
		if err != nil {
			return "0"
		}
		err = tx.QueryRow(`SELECT next_val FROM id_sequences WHERE name = 'global'`).Scan(&nextVal)
		if err != nil {
			return "0"
		}
		if err := tx.Commit(); err != nil {
			return "0"
		}
	}

	return agent.FormatIntBase62(nextVal)
}

// SeedSequences inserts the initial global row if it does not exist.
// Call after InitSchema to ensure the sequences table is populated.
func (g *SQLIDGenerator) SeedSequences() error {
	switch g.dialect {
	case Postgres:
		_, err := g.db.Exec(`INSERT INTO id_sequences (name, next_val) VALUES ('global', 0) ON CONFLICT DO NOTHING`)
		return err
	default:
		_, err := g.db.Exec(`INSERT OR IGNORE INTO id_sequences (name, next_val) VALUES ('global', 0)`)
		if err != nil {
			return fmt.Errorf("seed sequences: %w", err)
		}
		return nil
	}
}
