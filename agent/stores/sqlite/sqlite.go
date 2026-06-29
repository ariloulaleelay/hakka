package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ariloulaleelay/hakka/agent"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := path
	pragma := "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if strings.Contains(path, "?") {
		dsn = path + "&" + pragma
	} else {
		dsn = path + "?" + pragma
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite schema: %w", err)
	}
	runMigration(db, `ALTER TABLE sessions ADD COLUMN name TEXT NOT NULL DEFAULT ''`)
	runMigration(db, `ALTER TABLE sessions ADD COLUMN enabled_tools TEXT NOT NULL DEFAULT ''`)
	runMigration(db, `ALTER TABLE sessions ADD COLUMN blocked_tools TEXT NOT NULL DEFAULT ''`)
	runMigration(db, `ALTER TABLE sessions ADD COLUMN compact_soft_limit INTEGER NOT NULL DEFAULT 200000`)
	runMigration(db, `ALTER TABLE sessions ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`)
	runMigration(db, `ALTER TABLE sessions ADD COLUMN estimated_context_tokens INTEGER NOT NULL DEFAULT 0`)
	return &Store{db: db}, nil
}

func (st *Store) Close() error {
	return st.db.Close()
}

func runMigration(db *sql.DB, stmt string) {
	_, err := db.ExecContext(context.Background(), stmt)
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "duplicate column") {
		return
	}
	slog.Error("sqlite migration failed", "stmt", stmt, "error", err)
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	namespace     TEXT NOT NULL,
	id            TEXT NOT NULL,
	system_prompt TEXT NOT NULL,
	messages      TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL DEFAULT '',
	client_cwd    TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	total_tokens  INTEGER NOT NULL DEFAULT 0,
	name          TEXT NOT NULL DEFAULT '',
	enabled_tools TEXT NOT NULL DEFAULT '',
	blocked_tools TEXT NOT NULL DEFAULT '',
	compact_soft_limit INTEGER NOT NULL DEFAULT 200000,
	estimated_context_tokens INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (namespace, id)
);
CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions(updated_at);
`

// scanRow scans a SQL row into a *agent.Session via SessionData.
func scanRow(scanner interface{ Scan(dest ...any) error }) (*agent.Session, error) {
	var (
		data                  agent.SessionData
		messagesJSON          string
		createdText           string
		updatedText           string
		modelStr              string
		totalTokens           int
		enabledStr            string
		blockedStr            string
		compactSoftLim        int
		estimatedContextTokens int
	)
	err := scanner.Scan(
		&data.Namespace, &data.ID, &data.SystemPrompt,
		&messagesJSON, &createdText, &updatedText,
		&data.ClientCWD, &modelStr, &totalTokens,
		&data.Name, &enabledStr, &blockedStr, &compactSoftLim,
		&estimatedContextTokens,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(messagesJSON), &data.Messages); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}
	if enabledStr != "" {
		if err := json.Unmarshal([]byte(enabledStr), &data.EnabledTools); err != nil {
			data.EnabledTools = nil
		}
	}
	if blockedStr != "" {
		if err := json.Unmarshal([]byte(blockedStr), &data.BlockedTools); err != nil {
			data.BlockedTools = nil
		}
	}
	data.Model = modelStr
	data.TotalTokens = totalTokens
	data.CompactSoftLimit = compactSoftLim
	data.EstimatedContextTokens = estimatedContextTokens
	if err := data.CreatedAt.UnmarshalText([]byte(createdText)); err != nil {
		return nil, fmt.Errorf("decode created_at: %w", err)
	}
	if updatedText != "" {
		if err := data.UpdatedAt.UnmarshalText([]byte(updatedText)); err != nil {
			return nil, fmt.Errorf("decode updated_at: %w", err)
		}
	}
	return agent.NewSessionFromData(&data), nil
}

func (st *Store) Get(ctx context.Context, namespace, id string) (*agent.Session, bool, error) {
	row := st.db.QueryRowContext(ctx,
		`SELECT namespace, id, system_prompt, messages, created_at, updated_at, client_cwd, model, total_tokens, name, enabled_tools, blocked_tools, compact_soft_limit, estimated_context_tokens FROM sessions WHERE namespace = ? AND id = ?`, namespace, id)

	session, err := scanRow(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return session, true, nil
}

func (st *Store) Put(ctx context.Context, namespace string, session *agent.Session) error {
	data := session.Read()
	data.UpdatedAt = time.Now()

	messagesJSON, err := json.Marshal(data.Messages)
	if err != nil {
		return err
	}
	createdText, err := data.CreatedAt.MarshalText()
	if err != nil {
		return err
	}
	updatedAt := data.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = data.CreatedAt
	}
	updatedText, err := updatedAt.MarshalText()
	if err != nil {
		return err
	}

	enabledJSON := "{}"
	if len(data.EnabledTools) > 0 {
		b, err := json.Marshal(data.EnabledTools)
		if err != nil {
			slog.Error("sqlite: failed to marshal enabled_tools, saving with empty config",
				"session", data.ID, "error", err)
		} else {
			enabledJSON = string(b)
		}
	}

	blockedJSON := "{}"
	if len(data.BlockedTools) > 0 {
		b, err := json.Marshal(data.BlockedTools)
		if err != nil {
			slog.Error("sqlite: failed to marshal blocked_tools, saving with empty config",
				"session", data.ID, "error", err)
		} else {
			blockedJSON = string(b)
		}
	}

	_, err = st.db.ExecContext(ctx, `
		INSERT INTO sessions (namespace, id, system_prompt, messages, created_at, updated_at, client_cwd, model, total_tokens, name, enabled_tools, blocked_tools, compact_soft_limit, estimated_context_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, id) DO UPDATE SET
			system_prompt = excluded.system_prompt,
			messages      = excluded.messages,
			updated_at    = excluded.updated_at,
			client_cwd    = excluded.client_cwd,
			model         = excluded.model,
			total_tokens  = excluded.total_tokens,
			name          = excluded.name,
			enabled_tools = excluded.enabled_tools,
			blocked_tools = excluded.blocked_tools,
			compact_soft_limit = excluded.compact_soft_limit,
			estimated_context_tokens = excluded.estimated_context_tokens
	`, namespace, data.ID, data.SystemPrompt, string(messagesJSON), string(createdText), string(updatedText),
		data.ClientCWD, data.Model, data.TotalTokens, data.Name, enabledJSON, blockedJSON, data.CompactSoftLimit,
		data.EstimatedContextTokens)

	session.Update(func(d *agent.SessionData) {
		d.Namespace = namespace
		d.UpdatedAt = updatedAt
	})

	return err
}

func (st *Store) Delete(ctx context.Context, namespace, id string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM sessions WHERE namespace = ? AND id = ?`, namespace, id)
	return err
}

func (st *Store) List(ctx context.Context, namespace string) ([]*agent.Session, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT namespace, id, system_prompt, messages, created_at, updated_at, client_cwd, model, total_tokens, name, enabled_tools, blocked_tools, compact_soft_limit, estimated_context_tokens FROM sessions WHERE namespace = ? ORDER BY updated_at DESC`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*agent.Session
	for rows.Next() {
		session, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sessions, nil
}

var _ agent.SessionStore = (*Store)(nil)
