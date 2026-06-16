package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/ariloulaleelay/hakka/agent"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite schema: %w", err)
	}
	// Migration: add name column for older databases
	_, _ = db.ExecContext(context.Background(), `ALTER TABLE sessions ADD COLUMN name TEXT NOT NULL DEFAULT ''`)
	// Migration: add enabled_tools column for older databases
	_, _ = db.ExecContext(context.Background(), `ALTER TABLE sessions ADD COLUMN enabled_tools TEXT NOT NULL DEFAULT ''`)
	return &Store{db: db}, nil
}

func (st *Store) Close() error {
	return st.db.Close()
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	namespace     TEXT NOT NULL,
	id            TEXT NOT NULL,
	system_prompt TEXT NOT NULL,
	messages      TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	client_cwd    TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	total_tokens  INTEGER NOT NULL DEFAULT 0,
	name          TEXT NOT NULL DEFAULT '',
	enabled_tools TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (namespace, id)
);
`

func (st *Store) Get(ctx context.Context, namespace, id string) (*agent.Session, bool, error) {
	row := st.db.QueryRowContext(ctx,
		`SELECT namespace, id, system_prompt, messages, created_at, client_cwd, model, total_tokens, name, enabled_tools FROM sessions WHERE namespace = ? AND id = ?`, namespace, id)

	var (
		session       agent.Session
		messagesJSON  string
		createdText   string
		modelStr      string
		totalTokens   int
		enabledStr    string
	)
	err := row.Scan(&session.Namespace, &session.ID, &session.SystemPrompt, &messagesJSON, &createdText, &session.ClientCWD, &modelStr, &totalTokens, &session.Name, &enabledStr)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal([]byte(messagesJSON), &session.Messages); err != nil {
		return nil, false, fmt.Errorf("decode messages: %w", err)
	}
	// Restore enabled_tools
	if enabledStr != "" {
		if err := json.Unmarshal([]byte(enabledStr), &session.EnabledTools); err != nil {
			// Backward compat: if we can't parse, start with empty set
			session.EnabledTools = nil
		}
	}
	session.SetModel(modelStr)
	session.SetTotalTokenUsage(totalTokens)
	if err := session.CreatedAt.UnmarshalText([]byte(createdText)); err != nil {
		return nil, false, fmt.Errorf("decode created_at: %w", err)
	}
	return &session, true, nil
}

func (st *Store) Put(ctx context.Context, namespace string, session *agent.Session) error {
	messagesJSON, err := json.Marshal(session.Messages)
	if err != nil {
		return err
	}
	createdText, err := session.CreatedAt.MarshalText()
	if err != nil {
		return err
	}

	// Serialize enabled_tools to JSON (or empty string if nil/empty)
	enabledJSON := "{}"
	if len(session.EnabledTools) > 0 {
		if b, err := json.Marshal(session.EnabledTools); err == nil {
			enabledJSON = string(b)
		}
	}

	_, err = st.db.ExecContext(ctx, `
		INSERT INTO sessions (namespace, id, system_prompt, messages, created_at, client_cwd, model, total_tokens, name, enabled_tools)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, id) DO UPDATE SET
			system_prompt = excluded.system_prompt,
			messages      = excluded.messages,
			client_cwd    = excluded.client_cwd,
			model         = excluded.model,
			total_tokens  = excluded.total_tokens,
			name          = excluded.name,
			enabled_tools = excluded.enabled_tools
	`, namespace, session.ID, session.SystemPrompt, string(messagesJSON), string(createdText), session.ClientCWD, session.GetModel(), session.TotalTokenUsage(), session.Name, enabledJSON)
	return err
}

func (st *Store) Delete(ctx context.Context, namespace, id string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM sessions WHERE namespace = ? AND id = ?`, namespace, id)
	return err
}

func (st *Store) List(ctx context.Context, namespace string) ([]*agent.Session, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT namespace, id, system_prompt, messages, created_at, client_cwd, model, total_tokens, name, enabled_tools FROM sessions WHERE namespace = ? ORDER BY created_at ASC`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []*agent.Session
	for rows.Next() {
		var (
			session      agent.Session
			messagesJSON string
			createdText  string
			modelStr     string
			totalTokens  int
			enabledStr   string
		)
		err := rows.Scan(&session.Namespace, &session.ID, &session.SystemPrompt, &messagesJSON, &createdText, &session.ClientCWD, &modelStr, &totalTokens, &session.Name, &enabledStr)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(messagesJSON), &session.Messages); err != nil {
			return nil, fmt.Errorf("decode messages: %w", err)
		}
		if enabledStr != "" {
			if err := json.Unmarshal([]byte(enabledStr), &session.EnabledTools); err != nil {
				session.EnabledTools = nil
			}
		}
		session.SetModel(modelStr)
		session.SetTotalTokenUsage(totalTokens)
		if err := session.CreatedAt.UnmarshalText([]byte(createdText)); err != nil {
			return nil, fmt.Errorf("decode created_at: %w", err)
		}
		sessions = append(sessions, &session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sessions, nil
}

var _ agent.SessionStore = (*Store)(nil)