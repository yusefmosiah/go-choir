// Package store provides durable runtime storage for the go-choir sandbox runtime.
//
// The store persists task records and event records using SQLite, enabling
// stable task IDs and restart-safe task recovery (VAL-RUNTIME-003,
// VAL-RUNTIME-010). The schema is designed to migrate toward Dolt-backed
// per-user workspaces in later milestones.
//
// Design decisions:
//   - SQLite with WAL mode for concurrent read performance, matching the
//     existing auth store pattern.
//   - Single database file per sandbox (host-process milestone) rather than
//     per-user Dolt databases (that comes in the e-text milestone).
//   - Event sequence numbers are per-task, enabling incremental cursors for
//     the /api/events streaming surface.
//   - The store interface is minimal: CreateTask, GetTask, UpdateTask,
//     ListTasks, AppendEvent, ListEvents. Later features extend it.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// ErrNotFound is returned when a record is not found.
var ErrNotFound = errors.New("record not found")

// Store wraps a SQLite database connection and provides persistence for
// task records and event records.
type Store struct {
	db   *sql.DB
	path string
}

// schemaDDL creates the runtime tables if they do not already exist.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS tasks (
	task_id     TEXT PRIMARY KEY,
	owner_id    TEXT NOT NULL,
	sandbox_id  TEXT NOT NULL,
	state       TEXT NOT NULL,
	prompt      TEXT NOT NULL DEFAULT '',
	result      TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	created_at  DATETIME NOT NULL,
	updated_at  DATETIME NOT NULL,
	finished_at DATETIME,
	metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS events (
	event_id   TEXT NOT NULL,
	task_id    TEXT NOT NULL DEFAULT '',
	owner_id   TEXT NOT NULL DEFAULT '',
	seq        INTEGER NOT NULL,
	ts         DATETIME NOT NULL,
	kind       TEXT NOT NULL,
	phase      TEXT NOT NULL DEFAULT '',
	payload_json TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (event_id)
);

CREATE INDEX IF NOT EXISTS idx_tasks_owner_id ON tasks(owner_id);
CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);
CREATE INDEX IF NOT EXISTS idx_tasks_sandbox_id ON tasks(sandbox_id);
CREATE INDEX IF NOT EXISTS idx_events_task_id_seq ON events(task_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_owner_id ON events(owner_id);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
`

// Open opens (or creates) the SQLite database at dbPath and applies the
// runtime schema. It returns a Store ready for use.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("runtime store: create directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_busy_timeout=60000")
	if err != nil {
		return nil, fmt.Errorf("runtime store: open %s: %w", dbPath, err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runtime store: set WAL mode: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runtime store: enable foreign keys: %w", err)
	}

	// Limit concurrent connections for SQLite safety.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db, path: dbPath}
	if err := s.bootstrap(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runtime store: bootstrap: %w", err)
	}

	return s, nil
}

// bootstrap applies the schema DDL to the database.
func (s *Store) bootstrap() error {
	_, err := s.db.Exec(schemaDDL)
	if err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s.db != nil {
		// Checkpoint WAL before closing.
		_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		return s.db.Close()
	}
	return nil
}

// Path returns the database file path.
func (s *Store) Path() string {
	return s.path
}

// CreateTask inserts a new task record.
func (s *Store) CreateTask(ctx context.Context, rec types.TaskRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal task metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tasks (task_id, owner_id, sandbox_id, state, prompt, result, error, created_at, updated_at, finished_at, metadata_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.TaskID,
		rec.OwnerID,
		rec.SandboxID,
		rec.State,
		rec.Prompt,
		rec.Result,
		rec.Error,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.FinishedAt),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// GetTask returns the task with the given task ID.
func (s *Store) GetTask(ctx context.Context, taskID string) (types.TaskRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT task_id, owner_id, sandbox_id, state, prompt, result, error, created_at, updated_at, finished_at, metadata_json
		   FROM tasks
		  WHERE task_id = ?`,
		taskID,
	)
	return scanTask(row)
}

// UpdateTask updates an existing task record.
func (s *Store) UpdateTask(ctx context.Context, rec types.TaskRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal task metadata: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE tasks
		    SET owner_id = ?,
		        sandbox_id = ?,
		        state = ?,
		        prompt = ?,
		        result = ?,
		        error = ?,
		        updated_at = ?,
		        finished_at = ?,
		        metadata_json = ?
		  WHERE task_id = ?`,
		rec.OwnerID,
		rec.SandboxID,
		rec.State,
		rec.Prompt,
		rec.Result,
		rec.Error,
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.FinishedAt),
		string(metadata),
		rec.TaskID,
	)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated task rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: task %s", ErrNotFound, rec.TaskID)
	}
	return nil
}

// ListTasksByOwner returns tasks for the given owner, ordered by created_at
// descending, limited to the given count.
func (s *Store) ListTasksByOwner(ctx context.Context, ownerID string, limit int) ([]types.TaskRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listTasksWhere(ctx, "owner_id = ?", []any{ownerID}, limit)
}

// ListTasksByState returns tasks in the given state, ordered by created_at
// descending, limited to the given count.
func (s *Store) ListTasksByState(ctx context.Context, state types.TaskState, limit int) ([]types.TaskRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listTasksWhere(ctx, "state = ?", []any{string(state)}, limit)
}

// ListTasks returns recent tasks ordered by created_at descending, limited
// to the given count.
func (s *Store) ListTasks(ctx context.Context, limit int) ([]types.TaskRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listTasksWhere(ctx, "", nil, limit)
}

func (s *Store) listTasksWhere(ctx context.Context, where string, args []any, limit int) ([]types.TaskRecord, error) {
	query := `SELECT task_id, owner_id, sandbox_id, state, prompt, result, error, created_at, updated_at, finished_at, metadata_json
	            FROM tasks`
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tasks []types.TaskRecord
	for rows.Next() {
		rec, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, nil
}

// AppendEvent appends an event record with an auto-assigned sequence number.
// The Seq field on the input record is overwritten with the next sequence
// number for the task.
func (s *Store) AppendEvent(ctx context.Context, rec *types.EventRecord) error {
	if len(rec.Payload) == 0 {
		rec.Payload = json.RawMessage(`{}`)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Compute the next sequence number for this task.
	row := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE task_id = ?`,
		rec.TaskID,
	)
	if err := row.Scan(&rec.Seq); err != nil {
		return fmt.Errorf("query next event sequence: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (event_id, task_id, owner_id, seq, ts, kind, phase, payload_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.EventID,
		rec.TaskID,
		rec.OwnerID,
		rec.Seq,
		rec.Timestamp.UTC().Format(time.RFC3339Nano),
		rec.Kind,
		rec.Phase,
		string(rec.Payload),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit event insert: %w", err)
	}

	return nil
}

// ListEvents returns events for the given task, ordered by sequence ascending.
func (s *Store) ListEvents(ctx context.Context, taskID string, limit int) ([]types.EventRecord, error) {
	return s.ListEventsAfter(ctx, taskID, 0, limit)
}

// ListEventsAfter returns events for the given task with sequence > afterSeq,
// ordered by sequence ascending, limited to the given count.
func (s *Store) ListEventsAfter(ctx context.Context, taskID string, afterSeq int64, limit int) ([]types.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, task_id, owner_id, seq, ts, kind, phase, payload_json
		   FROM events
		  WHERE task_id = ?
		    AND seq > ?
		  ORDER BY seq ASC
		  LIMIT ?`,
		taskID,
		afterSeq,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []types.EventRecord
	for rows.Next() {
		rec, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

// ListEventsByOwner returns events for the given owner, ordered by timestamp
// descending, limited to the given count.
func (s *Store) ListEventsByOwner(ctx context.Context, ownerID string, limit int) ([]types.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, task_id, owner_id, seq, ts, kind, phase, payload_json
		   FROM events
		  WHERE owner_id = ?
		  ORDER BY ts DESC
		  LIMIT ?`,
		ownerID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query events by owner: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []types.EventRecord
	for rows.Next() {
		rec, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events by owner: %w", err)
	}
	return events, nil
}

// ListEventsByOwnerAfter returns events for the given owner with sequence >
// afterSeq across all tasks, ordered by timestamp ascending, limited to the
// given count. This supports SSE catch-up after reconnection where the client
// needs events newer than a previously seen sequence number.
func (s *Store) ListEventsByOwnerAfter(ctx context.Context, ownerID string, afterSeq int64, limit int) ([]types.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, task_id, owner_id, seq, ts, kind, phase, payload_json
		   FROM events
		  WHERE owner_id = ?
		    AND seq > ?
		  ORDER BY ts ASC
		  LIMIT ?`,
		ownerID,
		afterSeq,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query events by owner after seq: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []types.EventRecord
	for rows.Next() {
		rec, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events by owner after seq: %w", err)
	}
	return events, nil
}

// scanTask scans a task record from a single row.
func scanTask(row interface{ Scan(...any) error }) (types.TaskRecord, error) {
	var rec types.TaskRecord
	var createdAt, updatedAt string
	var finishedAt sql.NullString
	var metadataJSON string

	err := row.Scan(
		&rec.TaskID,
		&rec.OwnerID,
		&rec.SandboxID,
		&rec.State,
		&rec.Prompt,
		&rec.Result,
		&rec.Error,
		&createdAt,
		&updatedAt,
		&finishedAt,
		&metadataJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.TaskRecord{}, ErrNotFound
		}
		return types.TaskRecord{}, fmt.Errorf("scan task: %w", err)
	}

	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.TaskRecord{}, fmt.Errorf("parse created_at: %w", err)
	}
	rec.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return types.TaskRecord{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if finishedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			return types.TaskRecord{}, fmt.Errorf("parse finished_at: %w", err)
		}
		rec.FinishedAt = &t
	}

	if metadataJSON != "" && metadataJSON != "{}" {
		if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
			return types.TaskRecord{}, fmt.Errorf("parse metadata: %w", err)
		}
	}

	return rec, nil
}

// scanEvent scans an event record from a single row.
func scanEvent(row interface{ Scan(...any) error }) (types.EventRecord, error) {
	var rec types.EventRecord
	var ts string
	var payloadJSON string

	err := row.Scan(
		&rec.EventID,
		&rec.TaskID,
		&rec.OwnerID,
		&rec.Seq,
		&ts,
		&rec.Kind,
		&rec.Phase,
		&payloadJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.EventRecord{}, ErrNotFound
		}
		return types.EventRecord{}, fmt.Errorf("scan event: %w", err)
	}

	rec.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return types.EventRecord{}, fmt.Errorf("parse timestamp: %w", err)
	}
	rec.Payload = json.RawMessage(payloadJSON)

	return rec, nil
}

// marshalJSON marshals a value to JSON, returning "{}" for nil.
func marshalJSON(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}

// formatTimePtr formats a *time.Time for SQLite, returning nil for nil.
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}


