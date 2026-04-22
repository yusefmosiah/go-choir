// Package store provides durable runtime storage for the go-choir sandbox runtime.
//
// The store persists run records, agent records, channel messages, and event
// records using SQLite, enabling stable run IDs, durable agent/channel
// identity, and restart-safe recovery (VAL-RUNTIME-003,
// VAL-RUNTIME-010). The schema is designed to migrate toward Dolt-backed
// per-user workspaces in later milestones.
//
// Design decisions:
//   - SQLite with WAL mode for concurrent read performance, matching the
//     existing auth store pattern.
//   - Single database file per sandbox (host-process milestone) rather than
//     per-user Dolt databases (that comes in the vtext milestone).
//   - Event sequence numbers are per-task, enabling incremental cursors for
//     the /api/events streaming surface.
//   - The store interface is minimal: CreateRun, GetRun, UpdateRun,
//     ListRuns, AppendEvent, ListEvents. Later features extend it.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yusefmosiah/go-choir/internal/types"
)

// ErrNotFound is returned when a record is not found.
var ErrNotFound = errors.New("record not found")

// Store wraps a SQLite database connection and provides persistence for
// run records, agent records, channel messages, and event records.
type Store struct {
	db        *sql.DB
	path      string
	vtextDB   *sql.DB
	vtextPath string
}

// schemaDDL creates the runtime tables if they do not already exist.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS agents (
	agent_id    TEXT PRIMARY KEY,
	owner_id    TEXT NOT NULL,
	sandbox_id  TEXT NOT NULL,
	profile     TEXT NOT NULL DEFAULT '',
	role        TEXT NOT NULL DEFAULT '',
	channel_id  TEXT NOT NULL DEFAULT '',
	created_at  DATETIME NOT NULL,
	updated_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
	loop_id     TEXT PRIMARY KEY,
	agent_id    TEXT NOT NULL DEFAULT '',
	channel_id  TEXT NOT NULL DEFAULT '',
	parent_loop_id TEXT NOT NULL DEFAULT '',
	agent_profile TEXT NOT NULL DEFAULT '',
	agent_role TEXT NOT NULL DEFAULT '',
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
	loop_id    TEXT NOT NULL DEFAULT '',
	agent_id   TEXT NOT NULL DEFAULT '',
	channel_id TEXT NOT NULL DEFAULT '',
	owner_id   TEXT NOT NULL DEFAULT '',
	seq        INTEGER NOT NULL,
	ts         DATETIME NOT NULL,
	kind       TEXT NOT NULL,
	phase      TEXT NOT NULL DEFAULT '',
	payload_json TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (event_id)
);

CREATE TABLE IF NOT EXISTS channel_messages (
	channel_id      TEXT NOT NULL,
	seq             INTEGER NOT NULL,
	owner_id        TEXT NOT NULL DEFAULT '',
	from_agent_id   TEXT NOT NULL DEFAULT '',
	from_loop_id    TEXT NOT NULL DEFAULT '',
	to_agent_id     TEXT NOT NULL DEFAULT '',
	to_loop_id      TEXT NOT NULL DEFAULT '',
	trajectory_id   TEXT NOT NULL DEFAULT '',
	from_name       TEXT NOT NULL DEFAULT '',
	role            TEXT NOT NULL DEFAULT '',
	content         TEXT NOT NULL,
	created_at      DATETIME NOT NULL,
	PRIMARY KEY (channel_id, seq)
);

CREATE TABLE IF NOT EXISTS inbox_deliveries (
	delivery_id          TEXT PRIMARY KEY,
	owner_id             TEXT NOT NULL DEFAULT '',
	to_agent_id          TEXT NOT NULL DEFAULT '',
	to_loop_id           TEXT NOT NULL DEFAULT '',
	from_agent_id        TEXT NOT NULL DEFAULT '',
	from_loop_id         TEXT NOT NULL DEFAULT '',
	channel_id           TEXT NOT NULL DEFAULT '',
	role                 TEXT NOT NULL DEFAULT '',
	content              TEXT NOT NULL,
	trajectory_id        TEXT NOT NULL DEFAULT '',
	created_at           DATETIME NOT NULL,
	delivered_to_loop_id TEXT NOT NULL DEFAULT '',
	delivered_at         DATETIME
);

CREATE TABLE IF NOT EXISTS research_findings (
	owner_id          TEXT NOT NULL DEFAULT '',
	finding_id        TEXT NOT NULL DEFAULT '',
	agent_id          TEXT NOT NULL DEFAULT '',
	target_agent_id   TEXT NOT NULL DEFAULT '',
	channel_id        TEXT NOT NULL DEFAULT '',
	message_seq       INTEGER NOT NULL DEFAULT 0,
	trajectory_id     TEXT NOT NULL DEFAULT '',
	findings_json     TEXT NOT NULL DEFAULT '[]',
	evidence_ids_json TEXT NOT NULL DEFAULT '[]',
	notes_json        TEXT NOT NULL DEFAULT '[]',
	questions_json    TEXT NOT NULL DEFAULT '[]',
	content           TEXT NOT NULL DEFAULT '',
	created_at        DATETIME NOT NULL,
	PRIMARY KEY (owner_id, finding_id)
);

CREATE INDEX IF NOT EXISTS idx_agents_owner_id ON agents(owner_id);
CREATE INDEX IF NOT EXISTS idx_agents_channel_id ON agents(channel_id);
CREATE INDEX IF NOT EXISTS idx_runs_owner_id ON runs(owner_id);
CREATE INDEX IF NOT EXISTS idx_runs_state ON runs(state);
CREATE INDEX IF NOT EXISTS idx_runs_sandbox_id ON runs(sandbox_id);
CREATE INDEX IF NOT EXISTS idx_runs_agent_id ON runs(agent_id);
CREATE INDEX IF NOT EXISTS idx_runs_channel_id ON runs(channel_id);
CREATE INDEX IF NOT EXISTS idx_runs_parent_loop_id ON runs(parent_loop_id);
CREATE INDEX IF NOT EXISTS idx_events_loop_id_seq ON events(loop_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_owner_id ON events(owner_id);
CREATE INDEX IF NOT EXISTS idx_events_agent_id ON events(agent_id);
CREATE INDEX IF NOT EXISTS idx_events_channel_id_ts ON events(channel_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS idx_channel_messages_owner_id ON channel_messages(owner_id);
CREATE INDEX IF NOT EXISTS idx_channel_messages_created_at ON channel_messages(created_at);
CREATE INDEX IF NOT EXISTS idx_channel_messages_to_agent_id ON channel_messages(to_agent_id);
CREATE INDEX IF NOT EXISTS idx_channel_messages_trajectory_id ON channel_messages(trajectory_id);
CREATE INDEX IF NOT EXISTS idx_inbox_deliveries_owner_target ON inbox_deliveries(owner_id, to_agent_id, delivered_at);
CREATE INDEX IF NOT EXISTS idx_inbox_deliveries_created_at ON inbox_deliveries(created_at);
CREATE INDEX IF NOT EXISTS idx_research_findings_channel_id ON research_findings(channel_id, created_at);
CREATE INDEX IF NOT EXISTS idx_research_findings_target_agent_id ON research_findings(target_agent_id, created_at);

CREATE TABLE IF NOT EXISTS desktop_state (
	owner_id       TEXT PRIMARY KEY,
	windows_json   TEXT NOT NULL DEFAULT '[]',
	active_window  TEXT NOT NULL DEFAULT '',
	updated_at     DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS desktop_workspaces (
	owner_id       TEXT NOT NULL,
	desktop_id     TEXT NOT NULL,
	windows_json   TEXT NOT NULL DEFAULT '[]',
	active_window  TEXT NOT NULL DEFAULT '',
	updated_at     DATETIME NOT NULL,
	PRIMARY KEY (owner_id, desktop_id)
);

CREATE INDEX IF NOT EXISTS idx_desktop_workspaces_owner_id ON desktop_workspaces(owner_id);
`

// Open opens (or creates) the SQLite database at dbPath and applies the
// runtime schema. It returns a Store ready for use.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("runtime store: create directory: %w", err)
	}

	// If the runtime SQLite file has been removed, treat this as a fresh store
	// and clear any stale sibling vtext workspace left behind by a previous run.
	// This keeps repeat test runs isolated while preserving reopen semantics for
	// callers that keep the runtime DB file in place.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		_ = os.RemoveAll(deriveVTextWorkspacePath(dbPath))
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

	vtextDB, vtextPath, err := openVTextWorkspaceDB(dbPath)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runtime store: open vtext workspace: %w", err)
	}
	s.vtextDB = vtextDB
	s.vtextPath = vtextPath

	// Apply the vtext schema to the embedded Dolt workspace.
	if err := s.EnsureVTextSchema(); err != nil {
		_ = vtextDB.Close()
		_ = db.Close()
		return nil, fmt.Errorf("runtime store: bootstrap vtext: %w", err)
	}

	return s, nil
}

// bootstrap applies the schema DDL to the database.
func (s *Store) bootstrap() error {
	_, err := s.db.Exec(schemaDDL)
	if err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	for _, migration := range []struct {
		table string
		name  string
		ddl   string
	}{
		{"runs", "agent_id", "TEXT NOT NULL DEFAULT ''"},
		{"runs", "channel_id", "TEXT NOT NULL DEFAULT ''"},
		{"runs", "parent_loop_id", "TEXT NOT NULL DEFAULT ''"},
		{"runs", "agent_profile", "TEXT NOT NULL DEFAULT ''"},
		{"runs", "agent_role", "TEXT NOT NULL DEFAULT ''"},
		{"events", "agent_id", "TEXT NOT NULL DEFAULT ''"},
		{"events", "channel_id", "TEXT NOT NULL DEFAULT ''"},
		{"channel_messages", "to_agent_id", "TEXT NOT NULL DEFAULT ''"},
		{"channel_messages", "to_loop_id", "TEXT NOT NULL DEFAULT ''"},
		{"channel_messages", "trajectory_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(migration.table, migration.name, migration.ddl); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`
		INSERT INTO desktop_workspaces (owner_id, desktop_id, windows_json, active_window, updated_at)
		SELECT owner_id, 'primary', windows_json, active_window, updated_at
		  FROM desktop_state
		 WHERE NOT EXISTS (
			SELECT 1
			  FROM desktop_workspaces dw
			 WHERE dw.owner_id = desktop_state.owner_id
			   AND dw.desktop_id = 'primary'
		 )`); err != nil {
		return fmt.Errorf("migrate desktop_state to desktop_workspaces: %w", err)
	}
	return nil
}

func normalizeDesktopID(desktopID string) string {
	desktopID = strings.TrimSpace(desktopID)
	if desktopID == "" {
		return types.PrimaryDesktopID
	}
	return desktopID
}

func (s *Store) ensureColumn(table, name, ddl string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid      int
			column   string
			colType  string
			notNull  int
			defaultV sql.NullString
			primaryK int
		)
		if err := rows.Scan(&cid, &column, &colType, &notNull, &defaultV, &primaryK); err != nil {
			return fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		if column == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info(%s): %w", table, err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, name, ddl)); err != nil {
		return fmt.Errorf("alter table %s add column %s: %w", table, name, err)
	}
	return nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	var err error
	if s.vtextDB != nil {
		func() {
			defer func() {
				if r := recover(); r != nil && err == nil {
					err = fmt.Errorf("close vtext workspace: %v", r)
				}
			}()
			if closeErr := s.vtextDB.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}()
	}
	if s.db != nil {
		// Checkpoint WAL before closing.
		_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		if closeErr := s.db.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

// Path returns the database file path.
func (s *Store) Path() string {
	return s.path
}

// VTextPath returns the filesystem path backing the embedded vtext workspace.
func (s *Store) VTextPath() string {
	return s.vtextPath
}

// UpsertAgent persists a durable agent record.
func (s *Store) UpsertAgent(ctx context.Context, rec types.AgentRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (agent_id, owner_id, sandbox_id, profile, role, channel_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
		   owner_id = excluded.owner_id,
		   sandbox_id = excluded.sandbox_id,
		   profile = excluded.profile,
		   role = excluded.role,
		   channel_id = excluded.channel_id,
		   updated_at = excluded.updated_at`,
		rec.AgentID,
		rec.OwnerID,
		rec.SandboxID,
		rec.Profile,
		rec.Role,
		rec.ChannelID,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert agent: %w", err)
	}
	return nil
}

// GetAgent returns the agent with the given ID.
func (s *Store) GetAgent(ctx context.Context, agentID string) (types.AgentRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT agent_id, owner_id, sandbox_id, profile, role, channel_id, created_at, updated_at
		   FROM agents
		  WHERE agent_id = ?`,
		agentID,
	)
	return scanAgent(row)
}

// CreateRun inserts a new run record.
func (s *Store) CreateRun(ctx context.Context, rec types.RunRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal run metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO runs (loop_id, agent_id, channel_id, parent_loop_id, agent_profile, agent_role, owner_id, sandbox_id, state, prompt, result, error, created_at, updated_at, finished_at, metadata_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RunID,
		rec.AgentID,
		rec.ChannelID,
		rec.ParentRunID,
		rec.AgentProfile,
		rec.AgentRole,
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
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

// GetRun returns the run with the given run ID.
func (s *Store) GetRun(ctx context.Context, runID string) (types.RunRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT loop_id, agent_id, channel_id, parent_loop_id, agent_profile, agent_role, owner_id, sandbox_id, state, prompt, result, error, created_at, updated_at, finished_at, metadata_json
		   FROM runs
		  WHERE loop_id = ?`,
		runID,
	)
	return scanRun(row)
}

// UpdateRun updates an existing run record.
func (s *Store) UpdateRun(ctx context.Context, rec types.RunRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal run metadata: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE runs
		    SET agent_id = ?,
		        channel_id = ?,
		        parent_loop_id = ?,
		        agent_profile = ?,
		        agent_role = ?,
		        owner_id = ?,
		        sandbox_id = ?,
		        state = ?,
		        prompt = ?,
		        result = ?,
		        error = ?,
		        updated_at = ?,
		        finished_at = ?,
		        metadata_json = ?
		  WHERE loop_id = ?`,
		rec.AgentID,
		rec.ChannelID,
		rec.ParentRunID,
		rec.AgentProfile,
		rec.AgentRole,
		rec.OwnerID,
		rec.SandboxID,
		rec.State,
		rec.Prompt,
		rec.Result,
		rec.Error,
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.FinishedAt),
		string(metadata),
		rec.RunID,
	)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated run rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: run %s", ErrNotFound, rec.RunID)
	}
	return nil
}

// ListRunsByOwner returns runs for the given owner, ordered by created_at
// descending, limited to the given count.
func (s *Store) ListRunsByOwner(ctx context.Context, ownerID string, limit int) ([]types.RunRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listRunsWhere(ctx, "owner_id = ?", []any{ownerID}, limit)
}

// ListRunsByState returns runs in the given state, ordered by created_at
// descending, limited to the given count.
func (s *Store) ListRunsByState(ctx context.Context, state types.RunState, limit int) ([]types.RunRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listRunsWhere(ctx, "state = ?", []any{string(state)}, limit)
}

// ListRuns returns recent runs ordered by created_at descending, limited
// to the given count.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]types.RunRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listRunsWhere(ctx, "", nil, limit)
}

// ListRunsByChannel returns runs for a specific coordination channel, ordered by creation time descending.
func (s *Store) ListRunsByChannel(ctx context.Context, ownerID, channelID string, limit int) ([]types.RunRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.listRunsWhere(ctx, "owner_id = ? AND channel_id = ?", []any{ownerID, channelID}, limit)
}

// GetLatestActiveRunByAgent returns the most recent non-terminal run for an agent.
func (s *Store) GetLatestActiveRunByAgent(ctx context.Context, ownerID, agentID string) (types.RunRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT loop_id, agent_id, channel_id, parent_loop_id, agent_profile, agent_role, owner_id, sandbox_id, state, prompt, result, error, created_at, updated_at, finished_at, metadata_json
		   FROM runs
		  WHERE owner_id = ?
		    AND agent_id = ?
		    AND state IN ('pending', 'running', 'blocked')
		  ORDER BY updated_at DESC
		  LIMIT 1`,
		ownerID,
		agentID,
	)
	return scanRun(row)
}

func (s *Store) listRunsWhere(ctx context.Context, where string, args []any, limit int) ([]types.RunRecord, error) {
	query := `SELECT loop_id, agent_id, channel_id, parent_loop_id, agent_profile, agent_role, owner_id, sandbox_id, state, prompt, result, error, created_at, updated_at, finished_at, metadata_json
	            FROM runs`
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var runs []types.RunRecord
	for rows.Next() {
		rec, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runs: %w", err)
	}
	return runs, nil
}

// AppendEvent appends an event record with an auto-assigned sequence number.
// The Seq field on the input record is overwritten with the next sequence
// number for the run.
func (s *Store) AppendEvent(ctx context.Context, rec *types.EventRecord) error {
	if len(rec.Payload) == 0 {
		rec.Payload = json.RawMessage(`{}`)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Compute the next sequence number for this run.
	row := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE loop_id = ?`,
		rec.RunID,
	)
	if err := row.Scan(&rec.Seq); err != nil {
		return fmt.Errorf("query next event sequence: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (event_id, loop_id, agent_id, channel_id, owner_id, seq, ts, kind, phase, payload_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.EventID,
		rec.RunID,
		rec.AgentID,
		rec.ChannelID,
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

// ListEvents returns events for the given run, ordered by sequence ascending.
func (s *Store) ListEvents(ctx context.Context, runID string, limit int) ([]types.EventRecord, error) {
	return s.ListEventsAfter(ctx, runID, 0, limit)
}

// ListEventsAfter returns events for the given run with sequence > afterSeq,
// ordered by sequence ascending, limited to the given count.
func (s *Store) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]types.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, loop_id, agent_id, channel_id, owner_id, seq, ts, kind, phase, payload_json
		   FROM events
		  WHERE loop_id = ?
		    AND seq > ?
		  ORDER BY seq ASC
		  LIMIT ?`,
		runID,
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
		`SELECT event_id, loop_id, agent_id, channel_id, owner_id, seq, ts, kind, phase, payload_json
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
// afterSeq across all runs, ordered by timestamp ascending, limited to the
// given count. This supports SSE catch-up after reconnection where the client
// needs events newer than a previously seen sequence number.
func (s *Store) ListEventsByOwnerAfter(ctx context.Context, ownerID string, afterSeq int64, limit int) ([]types.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, loop_id, agent_id, channel_id, owner_id, seq, ts, kind, phase, payload_json
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

// ListEventsByChannel returns recent events for the given coordination channel.
func (s *Store) ListEventsByChannel(ctx context.Context, ownerID, channelID string, limit int) ([]types.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, loop_id, agent_id, channel_id, owner_id, seq, ts, kind, phase, payload_json
		   FROM events
		  WHERE owner_id = ?
		    AND channel_id = ?
		  ORDER BY ts ASC
		  LIMIT ?`,
		ownerID,
		channelID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query events by channel: %w", err)
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
		return nil, fmt.Errorf("iterate events by channel: %w", err)
	}
	return events, nil
}

// AppendChannelMessage persists a message to a coordination channel and assigns the next cursor sequence.
func (s *Store) AppendChannelMessage(ctx context.Context, message *types.ChannelMessage, ownerID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin channel message transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM channel_messages WHERE channel_id = ?`,
		message.ChannelID,
	)
	if err := row.Scan(&message.Seq); err != nil {
		return fmt.Errorf("query next channel message sequence: %w", err)
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now().UTC()
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO channel_messages (channel_id, seq, owner_id, from_agent_id, from_loop_id, to_agent_id, to_loop_id, trajectory_id, from_name, role, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ChannelID,
		message.Seq,
		ownerID,
		message.FromAgentID,
		message.FromRunID,
		message.ToAgentID,
		message.ToRunID,
		message.TrajectoryID,
		message.From,
		message.Role,
		message.Content,
		message.Timestamp.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert channel message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit channel message: %w", err)
	}
	return nil
}

// ListChannelMessages returns channel messages after the provided cursor, ordered by sequence ascending.
func (s *Store) ListChannelMessages(ctx context.Context, ownerID, channelID string, afterSeq int64, limit int) ([]types.ChannelMessage, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT channel_id, seq, from_agent_id, from_loop_id, to_agent_id, to_loop_id, trajectory_id, from_name, role, content, created_at
		   FROM channel_messages
		  WHERE channel_id = ?
		    AND seq > ?`
	args := []any{channelID, afterSeq}
	if strings.TrimSpace(ownerID) != "" {
		query += ` AND owner_id = ?`
		args = append(args, ownerID)
	}
	query += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query channel messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var messages []types.ChannelMessage
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel messages: %w", err)
	}
	return messages, nil
}

// EnqueueInboxDelivery persists a directed delivery for later runtime-owned
// threading into an agent loop.
func (s *Store) EnqueueInboxDelivery(ctx context.Context, delivery types.InboxDelivery) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO inbox_deliveries (delivery_id, owner_id, to_agent_id, to_loop_id, from_agent_id, from_loop_id, channel_id, role, content, trajectory_id, created_at, delivered_to_loop_id, delivered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		delivery.DeliveryID,
		delivery.OwnerID,
		delivery.ToAgentID,
		delivery.ToRunID,
		delivery.FromAgentID,
		delivery.FromRunID,
		delivery.ChannelID,
		delivery.Role,
		delivery.Content,
		delivery.TrajectoryID,
		delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
		delivery.DeliveredToLoopID,
		formatTimePtr(delivery.DeliveredAt),
	)
	if err != nil {
		return fmt.Errorf("insert inbox delivery: %w", err)
	}
	return nil
}

// ListPendingInboxDeliveries returns undelivered inbox items for the given
// target agent ordered by creation time.
func (s *Store) ListPendingInboxDeliveries(ctx context.Context, ownerID, toAgentID string, limit int) ([]types.InboxDelivery, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT delivery_id, owner_id, to_agent_id, to_loop_id, from_agent_id, from_loop_id, channel_id, role, content, trajectory_id, created_at, delivered_to_loop_id, delivered_at
		   FROM inbox_deliveries
		  WHERE owner_id = ?
		    AND to_agent_id = ?
		    AND delivered_at IS NULL
		  ORDER BY created_at ASC
		  LIMIT ?`,
		ownerID, toAgentID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query inbox deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []types.InboxDelivery
	for rows.Next() {
		rec, err := scanInboxDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox deliveries: %w", err)
	}
	return out, nil
}

// MarkInboxDeliveriesDelivered marks the given deliveries as consumed by the
// specified loop.
func (s *Store) MarkInboxDeliveriesDelivered(ctx context.Context, deliveryIDs []string, loopID string) error {
	if len(deliveryIDs) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(deliveryIDs)), ",")
	args := make([]any, 0, len(deliveryIDs)+3)
	args = append(args, loopID, now)
	for _, id := range deliveryIDs {
		args = append(args, id)
	}
	query := fmt.Sprintf(
		`UPDATE inbox_deliveries
		    SET delivered_to_loop_id = ?,
		        delivered_at = ?
		  WHERE delivery_id IN (%s)
		    AND delivered_at IS NULL
		    AND delivered_to_loop_id = ''`,
		placeholders,
	)
	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mark inbox deliveries delivered: %w", err)
	}
	return nil
}

// GetResearchFinding returns a previously dispatched researcher findings bundle.
func (s *Store) GetResearchFinding(ctx context.Context, ownerID, findingID string) (types.ResearchFindingRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT owner_id, finding_id, agent_id, target_agent_id, channel_id, message_seq, trajectory_id, findings_json, evidence_ids_json, notes_json, questions_json, content, created_at
		   FROM research_findings
		  WHERE owner_id = ? AND finding_id = ?`,
		ownerID, findingID,
	)
	return scanResearchFinding(row)
}

// DispatchResearchFinding atomically persists the addressed channel message,
// inbox delivery, and finding dispatch record inside the runtime SQLite store.
// Evidence durability remains in the vtext workspace and should be handled
// before calling this method with deterministic evidence IDs.
func (s *Store) DispatchResearchFinding(ctx context.Context, finding types.ResearchFindingRecord, message *types.ChannelMessage, delivery types.InboxDelivery) (types.ResearchFindingRecord, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("begin research finding transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scanResearchFinding(tx.QueryRowContext(ctx,
		`SELECT owner_id, finding_id, agent_id, target_agent_id, channel_id, message_seq, trajectory_id, findings_json, evidence_ids_json, notes_json, questions_json, content, created_at
		   FROM research_findings
		  WHERE owner_id = ? AND finding_id = ?`,
		finding.OwnerID, finding.FindingID,
	))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return types.ResearchFindingRecord{}, false, err
	}

	row := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM channel_messages WHERE channel_id = ?`,
		message.ChannelID,
	)
	if err := row.Scan(&message.Seq); err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("query next research finding message sequence: %w", err)
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now().UTC()
	}
	if delivery.CreatedAt.IsZero() {
		delivery.CreatedAt = message.Timestamp
	}
	finding.MessageSeq = message.Seq
	if finding.CreatedAt.IsZero() {
		finding.CreatedAt = message.Timestamp
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO channel_messages (channel_id, seq, owner_id, from_agent_id, from_loop_id, to_agent_id, to_loop_id, trajectory_id, from_name, role, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ChannelID,
		message.Seq,
		finding.OwnerID,
		message.FromAgentID,
		message.FromRunID,
		message.ToAgentID,
		message.ToRunID,
		message.TrajectoryID,
		message.From,
		message.Role,
		message.Content,
		message.Timestamp.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("insert research finding channel message: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO inbox_deliveries (delivery_id, owner_id, to_agent_id, to_loop_id, from_agent_id, from_loop_id, channel_id, role, content, trajectory_id, created_at, delivered_to_loop_id, delivered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		delivery.DeliveryID,
		delivery.OwnerID,
		delivery.ToAgentID,
		delivery.ToRunID,
		delivery.FromAgentID,
		delivery.FromRunID,
		delivery.ChannelID,
		delivery.Role,
		delivery.Content,
		delivery.TrajectoryID,
		delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
		delivery.DeliveredToLoopID,
		formatTimePtr(delivery.DeliveredAt),
	)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("insert research finding inbox delivery: %w", err)
	}

	findingsJSON, err := marshalStringSliceJSON(finding.Findings)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("marshal research findings findings: %w", err)
	}
	evidenceIDsJSON, err := marshalStringSliceJSON(finding.EvidenceIDs)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("marshal research findings evidence ids: %w", err)
	}
	notesJSON, err := marshalStringSliceJSON(finding.Notes)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("marshal research findings notes: %w", err)
	}
	questionsJSON, err := marshalStringSliceJSON(finding.Questions)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("marshal research findings questions: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO research_findings (owner_id, finding_id, agent_id, target_agent_id, channel_id, message_seq, trajectory_id, findings_json, evidence_ids_json, notes_json, questions_json, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		finding.OwnerID,
		finding.FindingID,
		finding.AgentID,
		finding.TargetAgentID,
		finding.ChannelID,
		finding.MessageSeq,
		finding.TrajectoryID,
		string(findingsJSON),
		string(evidenceIDsJSON),
		string(notesJSON),
		string(questionsJSON),
		finding.Content,
		finding.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("insert research finding record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return types.ResearchFindingRecord{}, false, fmt.Errorf("commit research finding transaction: %w", err)
	}
	return finding, true, nil
}

// scanRun scans a run record from a single row.
func scanRun(row interface{ Scan(...any) error }) (types.RunRecord, error) {
	var rec types.RunRecord
	var createdAt, updatedAt string
	var finishedAt sql.NullString
	var metadataJSON string

	err := row.Scan(
		&rec.RunID,
		&rec.AgentID,
		&rec.ChannelID,
		&rec.ParentRunID,
		&rec.AgentProfile,
		&rec.AgentRole,
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
			return types.RunRecord{}, ErrNotFound
		}
		return types.RunRecord{}, fmt.Errorf("scan run: %w", err)
	}

	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.RunRecord{}, fmt.Errorf("parse created_at: %w", err)
	}
	rec.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return types.RunRecord{}, fmt.Errorf("parse updated_at: %w", err)
	}
	if finishedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			return types.RunRecord{}, fmt.Errorf("parse finished_at: %w", err)
		}
		rec.FinishedAt = &t
	}

	if metadataJSON != "" && metadataJSON != "{}" {
		if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
			return types.RunRecord{}, fmt.Errorf("parse metadata: %w", err)
		}
	}

	return rec, nil
}

// scanAgent scans an agent record from a single row.
func scanAgent(row interface{ Scan(...any) error }) (types.AgentRecord, error) {
	var rec types.AgentRecord
	var createdAt, updatedAt string
	err := row.Scan(
		&rec.AgentID,
		&rec.OwnerID,
		&rec.SandboxID,
		&rec.Profile,
		&rec.Role,
		&rec.ChannelID,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.AgentRecord{}, ErrNotFound
		}
		return types.AgentRecord{}, fmt.Errorf("scan agent: %w", err)
	}
	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.AgentRecord{}, fmt.Errorf("parse agent created_at: %w", err)
	}
	rec.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return types.AgentRecord{}, fmt.Errorf("parse agent updated_at: %w", err)
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
		&rec.RunID,
		&rec.AgentID,
		&rec.ChannelID,
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

func scanChannelMessage(row interface{ Scan(...any) error }) (types.ChannelMessage, error) {
	var msg types.ChannelMessage
	var createdAt string
	err := row.Scan(
		&msg.ChannelID,
		&msg.Seq,
		&msg.FromAgentID,
		&msg.FromRunID,
		&msg.ToAgentID,
		&msg.ToRunID,
		&msg.TrajectoryID,
		&msg.From,
		&msg.Role,
		&msg.Content,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.ChannelMessage{}, ErrNotFound
		}
		return types.ChannelMessage{}, fmt.Errorf("scan channel message: %w", err)
	}
	msg.Timestamp, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.ChannelMessage{}, fmt.Errorf("parse channel message timestamp: %w", err)
	}
	return msg, nil
}

func scanInboxDelivery(row interface{ Scan(...any) error }) (types.InboxDelivery, error) {
	var rec types.InboxDelivery
	var createdAt string
	var deliveredAt sql.NullString
	err := row.Scan(
		&rec.DeliveryID,
		&rec.OwnerID,
		&rec.ToAgentID,
		&rec.ToRunID,
		&rec.FromAgentID,
		&rec.FromRunID,
		&rec.ChannelID,
		&rec.Role,
		&rec.Content,
		&rec.TrajectoryID,
		&createdAt,
		&rec.DeliveredToLoopID,
		&deliveredAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.InboxDelivery{}, ErrNotFound
		}
		return types.InboxDelivery{}, fmt.Errorf("scan inbox delivery: %w", err)
	}
	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.InboxDelivery{}, fmt.Errorf("parse inbox delivery created_at: %w", err)
	}
	if deliveredAt.Valid {
		ts, err := time.Parse(time.RFC3339Nano, deliveredAt.String)
		if err != nil {
			return types.InboxDelivery{}, fmt.Errorf("parse inbox delivery delivered_at: %w", err)
		}
		rec.DeliveredAt = &ts
	}
	return rec, nil
}

func scanResearchFinding(row interface{ Scan(...any) error }) (types.ResearchFindingRecord, error) {
	var (
		rec             types.ResearchFindingRecord
		findingsJSON    string
		evidenceIDsJSON string
		notesJSON       string
		questionsJSON   string
		createdAt       string
	)
	err := row.Scan(
		&rec.OwnerID,
		&rec.FindingID,
		&rec.AgentID,
		&rec.TargetAgentID,
		&rec.ChannelID,
		&rec.MessageSeq,
		&rec.TrajectoryID,
		&findingsJSON,
		&evidenceIDsJSON,
		&notesJSON,
		&questionsJSON,
		&rec.Content,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.ResearchFindingRecord{}, ErrNotFound
		}
		return types.ResearchFindingRecord{}, fmt.Errorf("scan research finding: %w", err)
	}
	if err := json.Unmarshal([]byte(findingsJSON), &rec.Findings); err != nil {
		return types.ResearchFindingRecord{}, fmt.Errorf("decode research finding findings: %w", err)
	}
	if err := json.Unmarshal([]byte(evidenceIDsJSON), &rec.EvidenceIDs); err != nil {
		return types.ResearchFindingRecord{}, fmt.Errorf("decode research finding evidence ids: %w", err)
	}
	if err := json.Unmarshal([]byte(notesJSON), &rec.Notes); err != nil {
		return types.ResearchFindingRecord{}, fmt.Errorf("decode research finding notes: %w", err)
	}
	if err := json.Unmarshal([]byte(questionsJSON), &rec.Questions); err != nil {
		return types.ResearchFindingRecord{}, fmt.Errorf("decode research finding questions: %w", err)
	}
	rec.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return types.ResearchFindingRecord{}, fmt.Errorf("parse research finding created_at: %w", err)
	}
	return rec, nil
}

func marshalStringSliceJSON(items []string) ([]byte, error) {
	if items == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(items)
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

// ----- Desktop state persistence (VAL-DESKTOP-007) -----

// GetDesktopState returns the persisted desktop state for the owner's primary
// desktop. If no state exists, it returns a default empty state with no error.
func (s *Store) GetDesktopState(ctx context.Context, ownerID string) (types.DesktopState, error) {
	return s.GetDesktopStateForDesktop(ctx, ownerID, types.PrimaryDesktopID)
}

// GetDesktopStateForDesktop returns the persisted desktop state for the given
// owner/desktop pair. If no state exists, it returns a default empty state.
func (s *Store) GetDesktopStateForDesktop(ctx context.Context, ownerID, desktopID string) (types.DesktopState, error) {
	desktopID = normalizeDesktopID(desktopID)
	var windowsJSON, updatedAt string
	var activeWindow string

	row := s.db.QueryRowContext(ctx,
		`SELECT windows_json, active_window, updated_at
		   FROM desktop_workspaces
		  WHERE owner_id = ? AND desktop_id = ?`,
		ownerID, desktopID,
	)

	err := row.Scan(&windowsJSON, &activeWindow, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No persisted state yet — return default empty state.
			return types.DesktopState{
				OwnerID:        ownerID,
				DesktopID:      desktopID,
				Windows:        []types.WindowState{},
				ActiveWindowID: "",
				UpdatedAt:      time.Now().UTC(),
			}, nil
		}
		return types.DesktopState{}, fmt.Errorf("query desktop state: %w", err)
	}

	var windows []types.WindowState
	if err := json.Unmarshal([]byte(windowsJSON), &windows); err != nil {
		return types.DesktopState{}, fmt.Errorf("unmarshal desktop windows: %w", err)
	}

	parsedTime, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		parsedTime = time.Now().UTC()
	}

	return types.DesktopState{
		OwnerID:        ownerID,
		DesktopID:      desktopID,
		Windows:        windows,
		ActiveWindowID: activeWindow,
		UpdatedAt:      parsedTime,
	}, nil
}

// SaveDesktopState persists the desktop state for the given owner's primary
// desktop. It uses UPSERT so that both initial save and subsequent updates work.
func (s *Store) SaveDesktopState(ctx context.Context, state types.DesktopState) error {
	return s.SaveDesktopStateForDesktop(ctx, state)
}

// SaveDesktopStateForDesktop persists the desktop state for the given
// owner/desktop pair using UPSERT.
func (s *Store) SaveDesktopStateForDesktop(ctx context.Context, state types.DesktopState) error {
	windowsJSON, err := json.Marshal(state.Windows)
	if err != nil {
		return fmt.Errorf("marshal desktop windows: %w", err)
	}
	desktopID := normalizeDesktopID(state.DesktopID)

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO desktop_workspaces (owner_id, desktop_id, windows_json, active_window, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(owner_id, desktop_id) DO UPDATE SET
		   windows_json = excluded.windows_json,
		   active_window = excluded.active_window,
		   updated_at = excluded.updated_at`,
		state.OwnerID,
		desktopID,
		string(windowsJSON),
		state.ActiveWindowID,
		state.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save desktop state: %w", err)
	}

	return nil
}
