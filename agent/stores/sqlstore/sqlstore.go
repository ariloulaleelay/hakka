// Package sqlstore provides shared SQL-backed SessionStore logic,
// parameterised by a database/sql.DB and a dialect for placeholder styles.
//
// Concrete wrappers live in agent/stores/sqlite and agent/stores/postgres.
package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/ariloulaleelay/hakka/agent"
)

// Dialect controls SQL dialect-specific behaviour.
type Dialect int

const (
	SQLite Dialect = iota
	Postgres
)

// Store implements agent.SessionStore backed by any *sql.DB.
type Store struct {
	db      *sql.DB
	dialect Dialect
}

// New creates a Store wrapping an already-open *sql.DB.
func New(db *sql.DB, dialect Dialect) *Store {
	return &Store{db: db, dialect: dialect}
}

// InitSchema creates tables if they don't exist.
func (s *Store) InitSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, s.sessionsDDL()); err != nil {
		return fmt.Errorf("create sessions table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, s.messagesDDL()); err != nil {
		return fmt.Errorf("create messages table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, s.indexesDDL()); err != nil {
		slog.Warn("sqlstore: failed to create indexes", "error", err)
	}
	return nil
}

// NeedsMigration returns true when the old-style "messages" column exists
// in the sessions table (pre-split schema). Only relevant for SQLite;
// Postgres deployments never had the old schema.
func (s *Store) NeedsMigration(ctx context.Context) (bool, error) {
	if s.dialect != SQLite {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		return false, fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, coltype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &coltype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "messages" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// MigrateFromOldSchema reads the old JSON "messages" column, inserts rows
// into the new messages table, then drops the old column.
func (s *Store) MigrateFromOldSchema(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT namespace, id, messages FROM sessions WHERE messages IS NOT NULL AND messages != ''`)
	if err != nil {
		return fmt.Errorf("query old sessions: %w", err)
	}
	defer rows.Close()

	type oldRow struct {
		ns       string
		id       string
		messages string
	}
	var oldRows []oldRow
	for rows.Next() {
		var r oldRow
		if err := rows.Scan(&r.ns, &r.id, &r.messages); err != nil {
			return fmt.Errorf("scan old row: %w", err)
		}
		oldRows = append(oldRows, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range oldRows {
		var msgs []agent.Message
		if err := json.Unmarshal([]byte(r.messages), &msgs); err != nil {
			return fmt.Errorf("unmarshal messages for %s/%s: %w", r.ns, r.id, err)
		}
		if err := s.insertMessages(ctx, r.ns, r.id, msgs); err != nil {
			return fmt.Errorf("insert messages for %s/%s: %w", r.ns, r.id, err)
		}
	}

	// Drop the old messages column (SQLite 3.35.0+, which modernc supports).
	if s.dialect == SQLite {
		_, err := s.db.ExecContext(ctx, `ALTER TABLE sessions DROP COLUMN messages`)
		if err != nil && !strings.Contains(err.Error(), "no such column") {
			slog.Warn("sqlstore: failed to drop old messages column", "error", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SessionStore implementation
// ---------------------------------------------------------------------------

func (s *Store) Get(ctx context.Context, namespace, id string) (*agent.Session, bool, error) {
	row := s.db.QueryRowContext(ctx, s.selectSession(), namespace, id)
	data, err := s.scanSession(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	msgs, err := s.loadMessages(ctx, namespace, id)
	if err != nil {
		return nil, false, err
	}
	data.Messages = msgs
	return agent.NewSessionFromData(data), true, nil
}

func (s *Store) Put(ctx context.Context, namespace string, session *agent.Session) error {
	data := session.Read()

	enabledJSON, err := marshalMap(data.EnabledTools)
	if err != nil {
		return err
	}
	blockedJSON, err := marshalMap(data.BlockedTools)
	if err != nil {
		return err
	}
	activeSkillsJSON, err := marshalSlice(data.ActiveSkills)
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
	streamingInt := 0
	if data.Streaming {
		streamingInt = 1
	}

	_, err = s.db.ExecContext(ctx, s.upsertSession(),
		namespace, data.ID, data.SystemPrompt, string(createdText), string(updatedText),
		data.ClientCWD, data.Model, data.TotalTokens,
		data.Name, enabledJSON, blockedJSON, data.CompactSoftLimit,
		data.EstimatedContextTokens, data.TotalCost, activeSkillsJSON, streamingInt,
	)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}

	if err := s.syncMessages(ctx, namespace, data.ID, data.Messages); err != nil {
		return fmt.Errorf("sync messages: %w", err)
	}

	session.Update(func(d *agent.SessionData) {
		d.Namespace = namespace
	})
	return nil
}

func (s *Store) Delete(ctx context.Context, namespace, id string) error {
	_, err := s.db.ExecContext(ctx, s.deleteSession(), namespace, id)
	return err
}

func (s *Store) List(ctx context.Context, namespace string) ([]*agent.Session, error) {
	rows, err := s.db.QueryContext(ctx, s.selectSessionsByNamespace(), namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*agent.Session
	for rows.Next() {
		data, err := s.scanSession(rows)
		if err != nil {
			return nil, err
		}
		msgs, err := s.loadMessages(ctx, data.Namespace, data.ID)
		if err != nil {
			return nil, err
		}
		data.Messages = msgs
		sessions = append(sessions, agent.NewSessionFromData(data))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Read().UpdatedAt.After(sessions[j].Read().UpdatedAt)
	})
	return sessions, nil
}

// ---------------------------------------------------------------------------
// DDL
// ---------------------------------------------------------------------------

func (s *Store) sessionsDDL() string {
	switch s.dialect {
	case Postgres:
		return `CREATE TABLE IF NOT EXISTS sessions (
	namespace      TEXT NOT NULL,
	id             TEXT NOT NULL,
	system_prompt  TEXT NOT NULL,
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL DEFAULT '',
	client_cwd     TEXT NOT NULL DEFAULT '',
	model          TEXT NOT NULL DEFAULT '',
	total_tokens   INTEGER NOT NULL DEFAULT 0,
	name           TEXT NOT NULL DEFAULT '',
	enabled_tools  TEXT NOT NULL DEFAULT '{}',
	blocked_tools  TEXT NOT NULL DEFAULT '{}',
	compact_soft_limit INTEGER NOT NULL DEFAULT 200000,
	estimated_context_tokens INTEGER NOT NULL DEFAULT 0,
	total_cost     DOUBLE PRECISION NOT NULL DEFAULT 0,
	active_skills  TEXT NOT NULL DEFAULT '[]',
	streaming      INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (namespace, id)
)`
	default:
		return `CREATE TABLE IF NOT EXISTS sessions (
	namespace      TEXT NOT NULL,
	id             TEXT NOT NULL,
	system_prompt  TEXT NOT NULL,
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL DEFAULT '',
	client_cwd     TEXT NOT NULL DEFAULT '',
	model          TEXT NOT NULL DEFAULT '',
	total_tokens   INTEGER NOT NULL DEFAULT 0,
	name           TEXT NOT NULL DEFAULT '',
	enabled_tools  TEXT NOT NULL DEFAULT '{}',
	blocked_tools  TEXT NOT NULL DEFAULT '{}',
	compact_soft_limit INTEGER NOT NULL DEFAULT 200000,
	estimated_context_tokens INTEGER NOT NULL DEFAULT 0,
	total_cost     REAL NOT NULL DEFAULT 0,
	active_skills  TEXT NOT NULL DEFAULT '[]',
	streaming      INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (namespace, id)
)`
	}
}

func (s *Store) messagesDDL() string {
	switch s.dialect {
	case Postgres:
		return `CREATE TABLE IF NOT EXISTS messages (
	session_ns   TEXT NOT NULL,
	session_id   TEXT NOT NULL,
	msg_index    INTEGER NOT NULL,
	role         TEXT NOT NULL,
	content      TEXT NOT NULL,
	tool_calls   TEXT,
	tool_call_id TEXT,
	name         TEXT,
	usage        TEXT,
	finish_reason TEXT,
	provider_metadata TEXT,
	timestamp    BIGINT,
	PRIMARY KEY (session_ns, session_id, msg_index),
	FOREIGN KEY (session_ns, session_id) REFERENCES sessions(namespace, id) ON DELETE CASCADE
)`
	default:
		return `CREATE TABLE IF NOT EXISTS messages (
	session_ns   TEXT NOT NULL,
	session_id   TEXT NOT NULL,
	msg_index    INTEGER NOT NULL,
	role         TEXT NOT NULL,
	content      TEXT NOT NULL,
	tool_calls   TEXT,
	tool_call_id TEXT,
	name         TEXT,
	usage        TEXT,
	finish_reason TEXT,
	provider_metadata TEXT,
	timestamp    INTEGER,
	PRIMARY KEY (session_ns, session_id, msg_index),
	FOREIGN KEY (session_ns, session_id) REFERENCES sessions(namespace, id) ON DELETE CASCADE
)`
	}
}

func (s *Store) indexesDDL() string {
	return `CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_ns, session_id)`
}

// ---------------------------------------------------------------------------
// Queries (dialect-aware)
// ---------------------------------------------------------------------------

func (s *Store) selectSession() string {
	switch s.dialect {
	case Postgres:
		return `SELECT namespace, id, system_prompt, created_at, updated_at, client_cwd,
			model, total_tokens, name, enabled_tools, blocked_tools,
			compact_soft_limit, estimated_context_tokens, total_cost,
			active_skills, streaming
		FROM sessions WHERE namespace = $1 AND id = $2`
	default:
		return `SELECT namespace, id, system_prompt, created_at, updated_at, client_cwd,
			model, total_tokens, name, enabled_tools, blocked_tools,
			compact_soft_limit, estimated_context_tokens, total_cost,
			active_skills, streaming
		FROM sessions WHERE namespace = ? AND id = ?`
	}
}

func (s *Store) selectSessionsByNamespace() string {
	switch s.dialect {
	case Postgres:
		return `SELECT namespace, id, system_prompt, created_at, updated_at, client_cwd,
			model, total_tokens, name, enabled_tools, blocked_tools,
			compact_soft_limit, estimated_context_tokens, total_cost,
			active_skills, streaming
		FROM sessions WHERE namespace = $1 ORDER BY updated_at DESC`
	default:
		return `SELECT namespace, id, system_prompt, created_at, updated_at, client_cwd,
			model, total_tokens, name, enabled_tools, blocked_tools,
			compact_soft_limit, estimated_context_tokens, total_cost,
			active_skills, streaming
		FROM sessions WHERE namespace = ? ORDER BY updated_at DESC`
	}
}

func (s *Store) upsertSession() string {
	switch s.dialect {
	case Postgres:
		return `INSERT INTO sessions (namespace, id, system_prompt, created_at, updated_at, client_cwd, model, total_tokens, name, enabled_tools, blocked_tools, compact_soft_limit, estimated_context_tokens, total_cost, active_skills, streaming)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT(namespace, id) DO UPDATE SET
			system_prompt = EXCLUDED.system_prompt,
			updated_at    = EXCLUDED.updated_at,
			client_cwd    = EXCLUDED.client_cwd,
			model         = EXCLUDED.model,
			total_tokens  = EXCLUDED.total_tokens,
			name          = EXCLUDED.name,
			enabled_tools = EXCLUDED.enabled_tools,
			blocked_tools = EXCLUDED.blocked_tools,
			compact_soft_limit = EXCLUDED.compact_soft_limit,
			estimated_context_tokens = EXCLUDED.estimated_context_tokens,
			total_cost    = EXCLUDED.total_cost,
			active_skills = EXCLUDED.active_skills,
			streaming     = EXCLUDED.streaming`
	default:
		return `INSERT INTO sessions (namespace, id, system_prompt, created_at, updated_at, client_cwd, model, total_tokens, name, enabled_tools, blocked_tools, compact_soft_limit, estimated_context_tokens, total_cost, active_skills, streaming)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, id) DO UPDATE SET
			system_prompt = excluded.system_prompt,
			updated_at    = excluded.updated_at,
			client_cwd    = excluded.client_cwd,
			model         = excluded.model,
			total_tokens  = excluded.total_tokens,
			name          = excluded.name,
			enabled_tools = excluded.enabled_tools,
			blocked_tools = excluded.blocked_tools,
			compact_soft_limit = excluded.compact_soft_limit,
			estimated_context_tokens = excluded.estimated_context_tokens,
			total_cost    = excluded.total_cost,
			active_skills = excluded.active_skills,
			streaming     = excluded.streaming`
	}
}

func (s *Store) deleteSession() string {
	switch s.dialect {
	case Postgres:
		return `DELETE FROM sessions WHERE namespace = $1 AND id = $2`
	default:
		return `DELETE FROM sessions WHERE namespace = ? AND id = ?`
	}
}

func (s *Store) selectMessages() string {
	switch s.dialect {
	case Postgres:
		return `SELECT role, content, tool_calls, tool_call_id, name, usage, finish_reason, provider_metadata, timestamp
		 FROM messages WHERE session_ns = $1 AND session_id = $2
		 ORDER BY msg_index ASC`
	default:
		return `SELECT role, content, tool_calls, tool_call_id, name, usage, finish_reason, provider_metadata, timestamp
		 FROM messages WHERE session_ns = ? AND session_id = ?
		 ORDER BY msg_index ASC`
	}
}

func (s *Store) deleteMessages() string {
	switch s.dialect {
	case Postgres:
		return `DELETE FROM messages WHERE session_ns = $1 AND session_id = $2`
	default:
		return `DELETE FROM messages WHERE session_ns = ? AND session_id = ?`
	}
}

func (s *Store) insertMessage() string {
	switch s.dialect {
	case Postgres:
		return `INSERT INTO messages (session_ns, session_id, msg_index, role, content, tool_calls, tool_call_id, name, usage, finish_reason, provider_metadata, timestamp)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	default:
		return `INSERT INTO messages (session_ns, session_id, msg_index, role, content, tool_calls, tool_call_id, name, usage, finish_reason, provider_metadata, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
}

// ---------------------------------------------------------------------------
// scan / message helpers
// ---------------------------------------------------------------------------

func (s *Store) scanSession(scanner interface{ Scan(dest ...any) error }) (*agent.SessionData, error) {
	var (
		data                   agent.SessionData
		createdText            string
		updatedText            string
		modelStr               string
		totalTokens            int
		totalCost              float64
		enabledStr             string
		blockedStr             string
		compactSoftLim         int
		estimatedContextTokens int
		activeSkillsStr        string
		streamingInt           int
	)
	err := scanner.Scan(
		&data.Namespace, &data.ID, &data.SystemPrompt,
		&createdText, &updatedText,
		&data.ClientCWD, &modelStr, &totalTokens,
		&data.Name, &enabledStr, &blockedStr, &compactSoftLim,
		&estimatedContextTokens, &totalCost, &activeSkillsStr,
		&streamingInt,
	)
	if err != nil {
		return nil, err
	}
	data.Streaming = streamingInt != 0
	data.Model = modelStr
	data.TotalTokens = totalTokens
	data.TotalCost = totalCost
	data.CompactSoftLimit = compactSoftLim
	data.EstimatedContextTokens = estimatedContextTokens

	if enabledStr != "" && enabledStr != "{}" {
		if err := json.Unmarshal([]byte(enabledStr), &data.EnabledTools); err != nil {
			data.EnabledTools = nil
		}
	}
	if blockedStr != "" && blockedStr != "{}" {
		if err := json.Unmarshal([]byte(blockedStr), &data.BlockedTools); err != nil {
			data.BlockedTools = nil
		}
	}
	if activeSkillsStr != "" && activeSkillsStr != "[]" {
		if err := json.Unmarshal([]byte(activeSkillsStr), &data.ActiveSkills); err != nil {
			data.ActiveSkills = nil
		}
	}
	if err := data.CreatedAt.UnmarshalText([]byte(createdText)); err != nil {
		return nil, fmt.Errorf("decode created_at: %w", err)
	}
	if updatedText != "" {
		if err := data.UpdatedAt.UnmarshalText([]byte(updatedText)); err != nil {
			return nil, fmt.Errorf("decode updated_at: %w", err)
		}
	}
	return &data, nil
}

func (s *Store) loadMessages(ctx context.Context, ns, id string) ([]agent.Message, error) {
	rows, err := s.db.QueryContext(ctx, s.selectMessages(), ns, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []agent.Message
	for rows.Next() {
		var (
			roleStr             string
			content             string
			toolCallsJSON       sql.NullString
			toolCallID          sql.NullString
			nameStr             sql.NullString
			usageJSON           sql.NullString
			finishReason        sql.NullString
			providerMetadataJSON sql.NullString
			ts                  sql.NullInt64
		)
		if err := rows.Scan(&roleStr, &content, &toolCallsJSON, &toolCallID,
			&nameStr, &usageJSON, &finishReason, &providerMetadataJSON, &ts); err != nil {
			return nil, err
		}
		msg := agent.Message{
			Role:    agent.Role(roleStr),
			Content: content,
		}
		if toolCallsJSON.Valid && toolCallsJSON.String != "" {
			if err := json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls); err != nil {
				return nil, fmt.Errorf("decode tool_calls: %w", err)
			}
		}
		if toolCallID.Valid {
			msg.ToolCallID = toolCallID.String
		}
		if nameStr.Valid {
			msg.Name = nameStr.String
		}
		if usageJSON.Valid && usageJSON.String != "" {
			var u agent.Usage
			if err := json.Unmarshal([]byte(usageJSON.String), &u); err != nil {
				return nil, fmt.Errorf("decode usage: %w", err)
			}
			msg.Usage = &u
		}
		if finishReason.Valid {
			msg.FinishReason = finishReason.String
		}
		if providerMetadataJSON.Valid && providerMetadataJSON.String != "" {
			if err := json.Unmarshal([]byte(providerMetadataJSON.String), &msg.ProviderMetadata); err != nil {
				return nil, fmt.Errorf("decode provider_metadata: %w", err)
			}
		}
		if ts.Valid {
			msg.Timestamp = ts.Int64
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func (s *Store) syncMessages(ctx context.Context, ns, id string, msgs []agent.Message) error {
	if _, err := s.db.ExecContext(ctx, s.deleteMessages(), ns, id); err != nil {
		return err
	}
	return s.insertMessages(ctx, ns, id, msgs)
}

func (s *Store) insertMessages(ctx context.Context, ns, id string, msgs []agent.Message) error {
	q := s.insertMessage()
	for i, msg := range msgs {
		_, err := s.db.ExecContext(ctx, q,
			ns, id, i, string(msg.Role), msg.Content,
			nullJSON(msg.ToolCalls),
			nullStr(msg.ToolCallID),
			nullStr(msg.Name),
			nullJSON(msg.Usage),
			nullStr(msg.FinishReason),
			nullJSON(msg.ProviderMetadata),
			nullInt64(msg.Timestamp),
		)
		if err != nil {
			return fmt.Errorf("insert message %d: %w", i, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func marshalMap(m map[string]bool) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		slog.Error("sqlstore: failed to marshal map", "error", err)
		return "{}", nil
	}
	return string(b), nil
}

func marshalSlice(s []string) (string, error) {
	if len(s) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		slog.Error("sqlstore: failed to marshal slice", "error", err)
		return "[]", nil
	}
	return string(b), nil
}

func nullJSON(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := string(b)
	if s == "null" || s == "[]" || s == "{}" {
		return nil
	}
	return s
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(n int64) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

var _ agent.SessionStore = (*Store)(nil)
