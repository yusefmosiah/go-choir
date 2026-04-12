package auth

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database connection and provides auth persistence for
// users, WebAuthn credentials, challenge/session state, and refresh/session
// records needed by later auth features.
type Store struct {
	db *sql.DB
}

// Schema DDL — all tables needed for Mission 2 Milestone 1 auth.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS users (
	id         TEXT PRIMARY KEY,
	email      TEXT UNIQUE NOT NULL,
	created_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS credentials (
	id              TEXT PRIMARY KEY,
	user_id         TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	public_key      BLOB    NOT NULL,
	attestation_type TEXT   NOT NULL,
	transport       TEXT    NOT NULL,
	sign_count      INTEGER NOT NULL DEFAULT 0,
	aaguid          BLOB    NOT NULL,
	flags           TEXT    NOT NULL DEFAULT '{}',
	created_at      DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS challenge_state (
	id                 TEXT PRIMARY KEY,
	user_id            TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	challenge          TEXT    NOT NULL,
	type               TEXT    NOT NULL CHECK(type IN ('registration', 'login')),
	allowed_credentials TEXT,
	webauthn_session_data TEXT,
	created_at         DATETIME NOT NULL,
	expires_at         DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS refresh_sessions (
	id           TEXT PRIMARY KEY,
	user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	token_hash   TEXT    NOT NULL,
	created_at   DATETIME NOT NULL,
	expires_at   DATETIME NOT NULL,
	rotated_from TEXT
);

CREATE INDEX IF NOT EXISTS idx_credentials_user_id ON credentials(user_id);
CREATE INDEX IF NOT EXISTS idx_challenge_state_user_id ON challenge_state(user_id);
CREATE INDEX IF NOT EXISTS idx_challenge_state_expires_at ON challenge_state(expires_at);
CREATE INDEX IF NOT EXISTS idx_refresh_sessions_user_id ON refresh_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_sessions_expires_at ON refresh_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_refresh_sessions_token_hash ON refresh_sessions(token_hash);
`

// schemaMigrations contains DDL statements that add columns to existing tables
// when the schema has evolved since the initial creation. These are run after
// the main DDL and are safe to repeat (they silently no-op if the column
// already exists due to the OR IGNORE / error-handling approach).
var schemaMigrations = []string{
	// Added webauthn_session_data column for storing serialized SessionData
	// needed by the finish handlers.
	`ALTER TABLE challenge_state ADD COLUMN webauthn_session_data TEXT`,
	// Added flags column for storing WebAuthn CredentialFlags (backup_eligible,
	// backup_state, user_present, user_verified) needed for re-login verification.
	`ALTER TABLE credentials ADD COLUMN flags TEXT NOT NULL DEFAULT '{}'`,
}

// User represents a row in the users table.
type User struct {
	ID        string
	Email     string
	CreatedAt time.Time
}

// Credential represents a WebAuthn passkey row in the credentials table.
type Credential struct {
	ID              string
	UserID          string
	PublicKey       []byte
	AttestationType string
	Transport       string
	SignCount       int64
	AAGUID          []byte
	Flags           string // JSON-encoded CredentialFlags: user_present, user_verified, backup_eligible, backup_state
	CreatedAt       time.Time
}

// ChallengeState represents a WebAuthn ceremony challenge row in the
// challenge_state table.
type ChallengeState struct {
	ID                  string
	UserID              string
	Challenge           string
	Type                string // "registration" or "login"
	AllowedCredentials  string // JSON-encoded array (may be empty for registration)
	WebAuthnSessionData string // JSON-serialized webauthn.SessionData for finish handlers
	CreatedAt           time.Time
	ExpiresAt           time.Time
}

// RefreshSession represents a refresh/session record in the
// refresh_sessions table.
type RefreshSession struct {
	ID          string
	UserID      string
	TokenHash   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RotatedFrom string
}

// OpenStore opens (or creates) the SQLite database at dbPath and applies the
// schema. It returns a Store ready for use.
func OpenStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("auth store: open %s: %w", dbPath, err)
	}

	// Enable WAL mode for better concurrent read performance and enable
	// foreign keys so that CASCADE works.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("auth store: set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("auth store: enable foreign keys: %w", err)
	}

	s := &Store{db: db}
	if err := s.bootstrap(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("auth store: bootstrap: %w", err)
	}

	return s, nil
}

// bootstrap applies the schema DDL to the database.
func (s *Store) bootstrap() error {
	_, err := s.db.Exec(schemaDDL)
	if err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	// Apply migrations — each ALTER TABLE may fail harmlessly if the column
	// already exists (SQLite returns "duplicate column name"). We ignore those
	// specific errors so that re-running bootstrap is idempotent.
	for _, m := range schemaMigrations {
		_, err := s.db.Exec(m)
		if err != nil {
			// SQLite returns "duplicate column name" when a column already exists.
			// This is safe to ignore.
			if !isDuplicateColumnErr(err) {
				return fmt.Errorf("apply migration %q: %w", m, err)
			}
		}
	}

	// Hard cutover: remove username column by recreating the users table.
	// This is idempotent and safe to run multiple times.
	if err := s.migrateDropUsernameColumn(); err != nil {
		return fmt.Errorf("migrate drop username column: %w", err)
	}

	return nil
}

// isDuplicateColumnErr returns true if the error is a SQLite "duplicate column
// name" error, which occurs when trying to add a column that already exists.
func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsString(msg, "duplicate column name")
}

// containsString reports whether substr is contained in s.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

// searchSubstring is a simple substring search.
func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// migrateDropUsernameColumn performs a hard cutover from the old schema
// (with username column) to the new schema (just email). SQLite doesn't
// support DROP COLUMN, so we recreate the table.
// This is idempotent: safe to run multiple times.
func (s *Store) migrateDropUsernameColumn() error {
	// Check if users table has a username column (old schema).
	var hasUsernameCol bool
	err := s.db.QueryRow(
		"SELECT 1 FROM pragma_table_info('users') WHERE name = 'username'",
	).Scan(&hasUsernameCol)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check for username column: %w", err)
	}

	// If no username column exists, we're already on the new schema — nothing to do.
	if !hasUsernameCol {
		return nil
	}

	// Also check if email column exists (for databases that have both columns).
	var hasEmailCol bool
	err = s.db.QueryRow(
		"SELECT 1 FROM pragma_table_info('users') WHERE name = 'email'",
	).Scan(&hasEmailCol)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check for email column: %w", err)
	}

	// SQLite doesn't support DROP COLUMN, so we recreate the table:
	// 1. Create new table with correct schema (no username)
	// 2. Copy data from old table (username → email, or email if it exists)
	// 3. Drop old table
	// 4. Rename new table
	//
	// Note: Foreign keys are temporarily disabled during this migration
	// because we're recreating the users table that other tables reference.

	// Disable foreign keys during table recreation.
	if _, err := s.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	// Re-enable foreign keys at the end (even on error paths).
	defer func() {
		_, _ = s.db.Exec("PRAGMA foreign_keys=ON")
	}()

	// Start transaction for atomicity.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Create new users table with correct schema.
	_, err = tx.Exec(`
		CREATE TABLE users_new (
			id         TEXT PRIMARY KEY,
			email      TEXT UNIQUE NOT NULL,
			created_at DATETIME NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("create users_new table: %w", err)
	}

	// Copy data from old table.
	// Use COALESCE(email, username) if email column exists, otherwise just username.
	var copySQL string
	if hasEmailCol {
		// Has both columns: prefer email, fall back to username if email is NULL.
		copySQL = `
			INSERT INTO users_new (id, email, created_at)
			SELECT id, COALESCE(email, username), created_at FROM users
		`
	} else {
		// Only has username column: use it directly for email.
		copySQL = `
			INSERT INTO users_new (id, email, created_at)
			SELECT id, username, created_at FROM users
		`
	}
	_, err = tx.Exec(copySQL)
	if err != nil {
		return fmt.Errorf("copy data to users_new: %w", err)
	}

	// Drop old table.
	_, err = tx.Exec("DROP TABLE users")
	if err != nil {
		return fmt.Errorf("drop old users table: %w", err)
	}

	// Rename new table to users.
	_, err = tx.Exec("ALTER TABLE users_new RENAME TO users")
	if err != nil {
		return fmt.Errorf("rename users_new to users: %w", err)
	}

	// Commit transaction.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration transaction: %w", err)
	}

	return nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// DB returns the underlying *sql.DB for use by later auth features that need
// direct query access.
func (s *Store) DB() *sql.DB {
	return s.db
}

// CreateUser inserts a new user and returns it.
func (s *Store) CreateUser(id, email string) (*User, error) {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		"INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)",
		id, email, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create user %q: %w", email, err)
	}
	return &User{ID: id, Email: email, CreatedAt: now}, nil
}

// GetUserByID returns the user with the given ID, or sql.ErrNoRows.
func (s *Store) GetUserByID(id string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		"SELECT id, email, created_at FROM users WHERE id = ?", id,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByEmail returns the user with the given email, or sql.ErrNoRows.
func (s *Store) GetUserByEmail(email string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		"SELECT id, email, created_at FROM users WHERE email = ?", email,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// CreateCredential inserts a WebAuthn credential (passkey) record.
func (s *Store) CreateCredential(c *Credential) error {
	_, err := s.db.Exec(
		"INSERT INTO credentials (id, user_id, public_key, attestation_type, transport, sign_count, aaguid, flags, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.ID, c.UserID, c.PublicKey, c.AttestationType, c.Transport, c.SignCount, c.AAGUID, c.Flags, c.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create credential %q: %w", c.ID, err)
	}
	return nil
}

// GetCredentialsByUserID returns all credentials for the given user.
func (s *Store) GetCredentialsByUserID(userID string) ([]Credential, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, public_key, attestation_type, transport, sign_count, aaguid, flags, created_at FROM credentials WHERE user_id = ?",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.UserID, &c.PublicKey, &c.AttestationType, &c.Transport, &c.SignCount, &c.AAGUID, &c.Flags, &c.CreatedAt); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

// UpdateCredentialSignCount updates the sign_count for the given credential ID.
func (s *Store) UpdateCredentialSignCount(credID string, signCount int64) error {
	_, err := s.db.Exec(
		"UPDATE credentials SET sign_count = ? WHERE id = ?",
		signCount, credID,
	)
	if err != nil {
		return fmt.Errorf("update credential sign count %q: %w", credID, err)
	}
	return nil
}

// SaveChallengeState inserts a challenge/session record for a WebAuthn ceremony.
func (s *Store) SaveChallengeState(cs *ChallengeState) error {
	_, err := s.db.Exec(
		"INSERT INTO challenge_state (id, user_id, challenge, type, allowed_credentials, webauthn_session_data, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		cs.ID, cs.UserID, cs.Challenge, cs.Type, cs.AllowedCredentials, cs.WebAuthnSessionData, cs.CreatedAt, cs.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("save challenge state %q: %w", cs.ID, err)
	}
	return nil
}

// GetChallengeStateByID returns the challenge state with the given ID.
func (s *Store) GetChallengeStateByID(id string) (*ChallengeState, error) {
	cs := &ChallengeState{}
	err := s.db.QueryRow(
		"SELECT id, user_id, challenge, type, allowed_credentials, webauthn_session_data, created_at, expires_at FROM challenge_state WHERE id = ?",
		id,
	).Scan(&cs.ID, &cs.UserID, &cs.Challenge, &cs.Type, &cs.AllowedCredentials, &cs.WebAuthnSessionData, &cs.CreatedAt, &cs.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return cs, nil
}

// DeleteChallengeStateByID removes a challenge state record (after finish or expiry).
func (s *Store) DeleteChallengeStateByID(id string) error {
	_, err := s.db.Exec("DELETE FROM challenge_state WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete challenge state %q: %w", id, err)
	}
	return nil
}

// GetChallengeStatesByUserID returns all challenge states for the given user,
// ordered by created_at descending (most recent first).
func (s *Store) GetChallengeStatesByUserID(userID string) ([]ChallengeState, error) {
	rows, err := s.db.Query(
		"SELECT id, user_id, challenge, type, allowed_credentials, webauthn_session_data, created_at, expires_at FROM challenge_state WHERE user_id = ? ORDER BY created_at DESC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []ChallengeState
	for rows.Next() {
		var cs ChallengeState
		if err := rows.Scan(&cs.ID, &cs.UserID, &cs.Challenge, &cs.Type, &cs.AllowedCredentials, &cs.WebAuthnSessionData, &cs.CreatedAt, &cs.ExpiresAt); err != nil {
			return nil, err
		}
		results = append(results, cs)
	}
	return results, rows.Err()
}

// CleanExpiredChallenges removes all challenge_state rows past their expires_at.
func (s *Store) CleanExpiredChallenges() (int64, error) {
	res, err := s.db.Exec("DELETE FROM challenge_state WHERE expires_at < ?", time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("clean expired challenges: %w", err)
	}
	return res.RowsAffected()
}

// CreateRefreshSession inserts a new refresh/session record.
func (s *Store) CreateRefreshSession(rs *RefreshSession) error {
	_, err := s.db.Exec(
		"INSERT INTO refresh_sessions (id, user_id, token_hash, created_at, expires_at, rotated_from) VALUES (?, ?, ?, ?, ?, ?)",
		rs.ID, rs.UserID, rs.TokenHash, rs.CreatedAt, rs.ExpiresAt, rs.RotatedFrom,
	)
	if err != nil {
		return fmt.Errorf("create refresh session %q: %w", rs.ID, err)
	}
	return nil
}

// GetRefreshSessionByTokenHash returns the refresh session matching the given
// token hash, or sql.ErrNoRows.
func (s *Store) GetRefreshSessionByTokenHash(tokenHash string) (*RefreshSession, error) {
	rs := &RefreshSession{}
	err := s.db.QueryRow(
		"SELECT id, user_id, token_hash, created_at, expires_at, rotated_from FROM refresh_sessions WHERE token_hash = ?",
		tokenHash,
	).Scan(&rs.ID, &rs.UserID, &rs.TokenHash, &rs.CreatedAt, &rs.ExpiresAt, &rs.RotatedFrom)
	if err != nil {
		return nil, err
	}
	return rs, nil
}

// DeleteRefreshSessionByID removes a refresh session by ID (used during
// rotation or logout).
func (s *Store) DeleteRefreshSessionByID(id string) error {
	_, err := s.db.Exec("DELETE FROM refresh_sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete refresh session %q: %w", id, err)
	}
	return nil
}

// DeleteRefreshSessionsByUserID removes all refresh sessions for a user
// (used during logout to fully invalidate).
func (s *Store) DeleteRefreshSessionsByUserID(userID string) error {
	_, err := s.db.Exec("DELETE FROM refresh_sessions WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("delete refresh sessions for user %q: %w", userID, err)
	}
	return nil
}

// CleanExpiredRefreshSessions removes all refresh_sessions rows past their
// expires_at.
func (s *Store) CleanExpiredRefreshSessions() (int64, error) {
	res, err := s.db.Exec("DELETE FROM refresh_sessions WHERE expires_at < ?", time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("clean expired refresh sessions: %w", err)
	}
	return res.RowsAffected()
}
