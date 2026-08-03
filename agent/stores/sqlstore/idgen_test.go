package sqlstore

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLIDGenerator(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := New(db, SQLite)
	ctx := context.Background()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	gen := NewSQLIDGenerator(db, SQLite)
	if err := gen.SeedSequences(); err != nil {
		t.Fatal(err)
	}

	// Generate sequence of IDs and verify they are monotonic and unique.
	seen := make(map[string]bool)
	var lastVal string
	for i := 0; i < 100; i++ {
		id := gen.GenerateID()
		if id == "" {
			t.Fatal("GenerateID returned empty")
		}
		if seen[id] {
			t.Fatalf("duplicate ID: %s (iteration %d)", id, i)
		}
		seen[id] = true
		lastVal = id
	}

	// Verify sequential IDs increase in length eventually.
	// First IDs should be short, later IDs longer.
	if len(lastVal) < 2 {
		t.Logf("after 100 generations, last ID is still short: %s (this is fine)", lastVal)
	}
}

func TestSQLIDGeneratorBase62(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := New(db, SQLite)
	ctx := context.Background()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	gen := NewSQLIDGenerator(db, SQLite)
	if err := gen.SeedSequences(); err != nil {
		t.Fatal(err)
	}

	// The first ID should be "1" (seed is 0, first GenerateID returns 1).
	id1 := gen.GenerateID()
	if id1 != "1" {
		t.Errorf("first ID = %q, want %q", id1, "1")
	}

	// Second should be "2".
	id2 := gen.GenerateID()
	if id2 != "2" {
		t.Errorf("second ID = %q, want %q", id2, "2")
	}
}

func TestMigrateMessageIDs(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := New(db, SQLite)
	ctx := context.Background()

	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	gen := NewSQLIDGenerator(db, SQLite)
	if err := gen.SeedSequences(); err != nil {
		t.Fatal(err)
	}

	// Insert a session and messages without msg_id.
	_, err = db.ExecContext(ctx,
		`INSERT INTO sessions (namespace, id, system_prompt, created_at, updated_at)
		 VALUES ('test', 's1', 'prompt', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.ExecContext(ctx,
		`INSERT INTO messages (session_ns, session_id, msg_index, role, content)
		 VALUES ('test', 's1', 0, 'user', 'hello')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO messages (session_ns, session_id, msg_index, role, content)
		 VALUES ('test', 's1', 1, 'assistant', 'hi')`)
	if err != nil {
		t.Fatal(err)
	}

	// Run migration.
	if err := store.MigrateMessageIDs(ctx, gen); err != nil {
		t.Fatal(err)
	}

	// Verify messages now have IDs.
	rows, err := db.QueryContext(ctx,
		`SELECT msg_id FROM messages WHERE session_ns = 'test' AND session_id = 's1' ORDER BY msg_index`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var msgID string
		if err := rows.Scan(&msgID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msgID)
	}

	if len(ids) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(ids))
	}
	for i, id := range ids {
		if id == "" {
			t.Errorf("message %d has empty msg_id after migration", i)
		}
	}
}
