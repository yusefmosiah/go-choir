package auth

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenStoreCreatesSchema(t *testing.T) {
	store := TestStore(t)

	// Verify that all tables exist by querying them.
	tables := []string{"users", "credentials", "challenge_state", "refresh_sessions"}
	for _, table := range tables {
		var name string
		err := store.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found in schema: %v", table, err)
		}
	}
}

func TestOpenStoreIdempotentBootstrap(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open and bootstrap once.
	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first OpenStore: %v", err)
	}
	store1.Close()

	// Reopen the same database — bootstrap should be idempotent (IF NOT EXISTS).
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second OpenStore: %v", err)
	}
	store2.Close()
}

func TestOpenStoreInvalidPath(t *testing.T) {
	// Try to open a database in a directory that doesn't exist and can't be created.
	_, err := OpenStore("/nonexistent/path/that/cannot/be/created/auth.db")
	if err == nil {
		t.Error("expected error for invalid DB path, got nil")
	}
}

func TestOpenStoreSetsWALAndForeignKeys(t *testing.T) {
	store := TestStore(t)

	var journalMode string
	if err := store.DB().QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode: got %q, want %q", journalMode, "wal")
	}

	var fkEnabled bool
	if err := store.DB().QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if !fkEnabled {
		t.Error("foreign_keys: got false, want true")
	}
}

// --- User CRUD ---

func TestCreateUser(t *testing.T) {
	store := TestStore(t)

	user, err := store.CreateUser("user-1", "alice")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != "user-1" {
		t.Errorf("ID: got %q, want %q", user.ID, "user-1")
	}
	if user.Username != "alice" {
		t.Errorf("Username: got %q, want %q", user.Username, "alice")
	}
	if user.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestCreateUserDuplicateUsername(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	_, err := store.CreateUser("user-2", "alice") // same username
	if err == nil {
		t.Error("expected error for duplicate username, got nil")
	}
}

func TestGetUserByID(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	user, err := store.GetUserByID("user-1")
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if user.Username != "alice" {
		t.Errorf("Username: got %q, want %q", user.Username, "alice")
	}
}

func TestGetUserByIDNotFound(t *testing.T) {
	store := TestStore(t)

	_, err := store.GetUserByID("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got: %v", err)
	}
}

func TestGetUserByUsername(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	user, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if user.ID != "user-1" {
		t.Errorf("ID: got %q, want %q", user.ID, "user-1")
	}
}

func TestGetUserByUsernameNotFound(t *testing.T) {
	store := TestStore(t)

	_, err := store.GetUserByUsername("nobody")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got: %v", err)
	}
}

// --- Credential CRUD ---

func TestCreateAndGetCredential(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cred := &Credential{
		ID:              "cred-1",
		UserID:          "user-1",
		PublicKey:       []byte("fake-public-key"),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	creds, err := store.GetCredentialsByUserID("user-1")
	if err != nil {
		t.Fatalf("GetCredentialsByUserID: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("credentials count: got %d, want 1", len(creds))
	}
	if creds[0].ID != "cred-1" {
		t.Errorf("ID: got %q, want %q", creds[0].ID, "cred-1")
	}
	if string(creds[0].PublicKey) != "fake-public-key" {
		t.Errorf("PublicKey: got %q, want %q", string(creds[0].PublicKey), "fake-public-key")
	}
}

func TestCreateCredentialMissingUser(t *testing.T) {
	store := TestStore(t)

	cred := &Credential{
		ID:              "cred-1",
		UserID:          "nonexistent-user",
		PublicKey:       []byte("key"),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		CreatedAt:       time.Now().UTC(),
	}
	err := store.CreateCredential(cred)
	if err == nil {
		t.Error("expected error for credential with missing user, got nil")
	}
}

func TestGetCredentialsEmpty(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	creds, err := store.GetCredentialsByUserID("user-1")
	if err != nil {
		t.Fatalf("GetCredentialsByUserID: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials, got %d", len(creds))
	}
}

func TestUpdateCredentialSignCount(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cred := &Credential{
		ID:              "cred-1",
		UserID:          "user-1",
		PublicKey:       []byte("key"),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	if err := store.UpdateCredentialSignCount("cred-1", 42); err != nil {
		t.Fatalf("UpdateCredentialSignCount: %v", err)
	}

	creds, err := store.GetCredentialsByUserID("user-1")
	if err != nil {
		t.Fatalf("GetCredentialsByUserID: %v", err)
	}
	if creds[0].SignCount != 42 {
		t.Errorf("SignCount: got %d, want 42", creds[0].SignCount)
	}
}

func TestCredentialCascadeDelete(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cred := &Credential{
		ID:              "cred-1",
		UserID:          "user-1",
		PublicKey:       []byte("key"),
		AttestationType: "none",
		Transport:       `["internal"]`,
		SignCount:       0,
		AAGUID:          make([]byte, 16),
		CreatedAt:       time.Now().UTC(),
	}
	if err := store.CreateCredential(cred); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	// Delete the user; credentials should cascade.
	_, err := store.DB().Exec("DELETE FROM users WHERE id = ?", "user-1")
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}

	creds, err := store.GetCredentialsByUserID("user-1")
	if err != nil {
		t.Fatalf("GetCredentialsByUserID: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials after user delete (cascade), got %d", len(creds))
	}
}

// --- ChallengeState CRUD ---

func TestSaveAndGetChallengeState(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cs := &ChallengeState{
		ID:        "challenge-1",
		UserID:    "user-1",
		Challenge: "dGVzdC1jaGFsbGVuZ2U",
		Type:      "registration",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(cs); err != nil {
		t.Fatalf("SaveChallengeState: %v", err)
	}

	got, err := store.GetChallengeStateByID("challenge-1")
	if err != nil {
		t.Fatalf("GetChallengeStateByID: %v", err)
	}
	if got.Challenge != cs.Challenge {
		t.Errorf("Challenge: got %q, want %q", got.Challenge, cs.Challenge)
	}
	if got.Type != "registration" {
		t.Errorf("Type: got %q, want %q", got.Type, "registration")
	}
}

func TestSaveChallengeStateInvalidType(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cs := &ChallengeState{
		ID:        "challenge-bad",
		UserID:    "user-1",
		Challenge: "challenge",
		Type:      "invalid-type", // not in CHECK constraint
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}
	err := store.SaveChallengeState(cs)
	if err == nil {
		t.Error("expected error for invalid challenge type, got nil")
	}
}

func TestDeleteChallengeState(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cs := &ChallengeState{
		ID:        "challenge-1",
		UserID:    "user-1",
		Challenge: "challenge",
		Type:      "login",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(cs); err != nil {
		t.Fatalf("SaveChallengeState: %v", err)
	}

	if err := store.DeleteChallengeStateByID("challenge-1"); err != nil {
		t.Fatalf("DeleteChallengeStateByID: %v", err)
	}

	_, err := store.GetChallengeStateByID("challenge-1")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got: %v", err)
	}
}

func TestCleanExpiredChallenges(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	now := time.Now().UTC()

	// Insert an expired challenge.
	expired := &ChallengeState{
		ID:        "expired-1",
		UserID:    "user-1",
		Challenge: "expired",
		Type:      "registration",
		CreatedAt: now.Add(-10 * time.Minute),
		ExpiresAt: now.Add(-5 * time.Minute), // already expired
	}
	if err := store.SaveChallengeState(expired); err != nil {
		t.Fatalf("SaveChallengeState expired: %v", err)
	}

	// Insert a valid challenge.
	valid := &ChallengeState{
		ID:        "valid-1",
		UserID:    "user-1",
		Challenge: "valid",
		Type:      "registration",
		CreatedAt: now,
		ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(valid); err != nil {
		t.Fatalf("SaveChallengeState valid: %v", err)
	}

	n, err := store.CleanExpiredChallenges()
	if err != nil {
		t.Fatalf("CleanExpiredChallenges: %v", err)
	}
	if n != 1 {
		t.Errorf("rows affected: got %d, want 1", n)
	}

	// Valid challenge should still exist.
	got, err := store.GetChallengeStateByID("valid-1")
	if err != nil {
		t.Fatalf("GetChallengeStateByID valid: %v", err)
	}
	if got.Challenge != "valid" {
		t.Errorf("valid challenge: got %q, want %q", got.Challenge, "valid")
	}

	// Expired challenge should be gone.
	_, err = store.GetChallengeStateByID("expired-1")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for expired challenge, got: %v", err)
	}
}

func TestChallengeStateCascadeDelete(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cs := &ChallengeState{
		ID:        "challenge-1",
		UserID:    "user-1",
		Challenge: "challenge",
		Type:      "registration",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(cs); err != nil {
		t.Fatalf("SaveChallengeState: %v", err)
	}

	// Delete the user; challenge state should cascade.
	_, err := store.DB().Exec("DELETE FROM users WHERE id = ?", "user-1")
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}

	_, err = store.GetChallengeStateByID("challenge-1")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after user cascade, got: %v", err)
	}
}

func TestChallengeStateLoginWithAllowedCredentials(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cs := &ChallengeState{
		ID:                 "login-challenge-1",
		UserID:             "user-1",
		Challenge:          "bG9naW4tY2hhbGxlbmdl",
		Type:               "login",
		AllowedCredentials: `["cred-1","cred-2"]`,
		CreatedAt:          time.Now().UTC(),
		ExpiresAt:          time.Now().UTC().Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(cs); err != nil {
		t.Fatalf("SaveChallengeState: %v", err)
	}

	got, err := store.GetChallengeStateByID("login-challenge-1")
	if err != nil {
		t.Fatalf("GetChallengeStateByID: %v", err)
	}
	if got.AllowedCredentials != `["cred-1","cred-2"]` {
		t.Errorf("AllowedCredentials: got %q, want %q", got.AllowedCredentials, `["cred-1","cred-2"]`)
	}
	if got.Type != "login" {
		t.Errorf("Type: got %q, want %q", got.Type, "login")
	}
}

func TestChallengeStateWithWebAuthnSessionData(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	sessionData := `{"challenge":"test-challenge","rpId":"localhost","user_id":"dXNlci0x"}`
	cs := &ChallengeState{
		ID:                 "challenge-wa",
		UserID:             "user-1",
		Challenge:          "test-challenge",
		Type:               "registration",
		WebAuthnSessionData: sessionData,
		CreatedAt:          time.Now().UTC(),
		ExpiresAt:          time.Now().UTC().Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(cs); err != nil {
		t.Fatalf("SaveChallengeState: %v", err)
	}

	got, err := store.GetChallengeStateByID("challenge-wa")
	if err != nil {
		t.Fatalf("GetChallengeStateByID: %v", err)
	}
	if got.WebAuthnSessionData != sessionData {
		t.Errorf("WebAuthnSessionData: got %q, want %q", got.WebAuthnSessionData, sessionData)
	}
}

func TestGetChallengeStatesByUserID(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create two challenges.
	cs1 := &ChallengeState{
		ID:        "challenge-1",
		UserID:    "user-1",
		Challenge: "challenge-1",
		Type:      "registration",
		CreatedAt: time.Now().UTC().Add(-1 * time.Minute),
		ExpiresAt: time.Now().UTC().Add(4 * time.Minute),
	}
	if err := store.SaveChallengeState(cs1); err != nil {
		t.Fatalf("SaveChallengeState 1: %v", err)
	}

	cs2 := &ChallengeState{
		ID:        "challenge-2",
		UserID:    "user-1",
		Challenge: "challenge-2",
		Type:      "login",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}
	if err := store.SaveChallengeState(cs2); err != nil {
		t.Fatalf("SaveChallengeState 2: %v", err)
	}

	results, err := store.GetChallengeStatesByUserID("user-1")
	if err != nil {
		t.Fatalf("GetChallengeStatesByUserID: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 challenges, got %d", len(results))
	}

	// Should be ordered by created_at descending (most recent first).
	if results[0].ID != "challenge-2" {
		t.Errorf("first result: got %q, want %q", results[0].ID, "challenge-2")
	}
	if results[1].ID != "challenge-1" {
		t.Errorf("second result: got %q, want %q", results[1].ID, "challenge-1")
	}
}

// --- RefreshSession CRUD ---

func TestCreateAndGetRefreshSession(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rs := &RefreshSession{
		ID:        "rs-1",
		UserID:    "user-1",
		TokenHash: "test-token-hash-1",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	if err := store.CreateRefreshSession(rs); err != nil {
		t.Fatalf("CreateRefreshSession: %v", err)
	}

	got, err := store.GetRefreshSessionByTokenHash("test-token-hash-1")
	if err != nil {
		t.Fatalf("GetRefreshSessionByTokenHash: %v", err)
	}
	if got.UserID != "user-1" {
		t.Errorf("UserID: got %q, want %q", got.UserID, "user-1")
	}
	if got.TokenHash != "test-token-hash-1" {
		t.Errorf("TokenHash: got %q, want %q", got.TokenHash, "test-token-hash-1")
	}
}

func TestCreateRefreshSessionWithRotation(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rs1 := &RefreshSession{
		ID:        "rs-1",
		UserID:    "user-1",
		TokenHash: "hash-1",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	if err := store.CreateRefreshSession(rs1); err != nil {
		t.Fatalf("CreateRefreshSession rs-1: %v", err)
	}

	rs2 := &RefreshSession{
		ID:          "rs-2",
		UserID:      "user-1",
		TokenHash:   "hash-2",
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(720 * time.Hour),
		RotatedFrom: "rs-1", // rotated from previous session
	}
	if err := store.CreateRefreshSession(rs2); err != nil {
		t.Fatalf("CreateRefreshSession rs-2: %v", err)
	}

	got, err := store.GetRefreshSessionByTokenHash("hash-2")
	if err != nil {
		t.Fatalf("GetRefreshSessionByTokenHash: %v", err)
	}
	if got.RotatedFrom != "rs-1" {
		t.Errorf("RotatedFrom: got %q, want %q", got.RotatedFrom, "rs-1")
	}
}

func TestDeleteRefreshSessionByID(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rs := &RefreshSession{
		ID:        "rs-1",
		UserID:    "user-1",
		TokenHash: "hash-1",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	if err := store.CreateRefreshSession(rs); err != nil {
		t.Fatalf("CreateRefreshSession: %v", err)
	}

	if err := store.DeleteRefreshSessionByID("rs-1"); err != nil {
		t.Fatalf("DeleteRefreshSessionByID: %v", err)
	}

	_, err := store.GetRefreshSessionByTokenHash("hash-1")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got: %v", err)
	}
}

func TestDeleteRefreshSessionsByUserID(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	for i := 0; i < 3; i++ {
		rs := &RefreshSession{
			ID:        fmt.Sprintf("rs-%d", i),
			UserID:    "user-1",
			TokenHash: fmt.Sprintf("hash-%d", i),
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
		}
		if err := store.CreateRefreshSession(rs); err != nil {
			t.Fatalf("CreateRefreshSession %d: %v", i, err)
		}
	}

	if err := store.DeleteRefreshSessionsByUserID("user-1"); err != nil {
		t.Fatalf("DeleteRefreshSessionsByUserID: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, err := store.GetRefreshSessionByTokenHash(fmt.Sprintf("hash-%d", i))
		if err != sql.ErrNoRows {
			t.Errorf("hash-%d: expected sql.ErrNoRows after user delete, got: %v", i, err)
		}
	}
}

func TestCleanExpiredRefreshSessions(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	now := time.Now().UTC()

	// Insert an expired session.
	expired := &RefreshSession{
		ID:        "expired-rs",
		UserID:    "user-1",
		TokenHash: "expired-hash",
		CreatedAt: now.Add(-800 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Hour), // already expired
	}
	if err := store.CreateRefreshSession(expired); err != nil {
		t.Fatalf("CreateRefreshSession expired: %v", err)
	}

	// Insert a valid session.
	valid := &RefreshSession{
		ID:        "valid-rs",
		UserID:    "user-1",
		TokenHash: "valid-hash",
		CreatedAt: now,
		ExpiresAt: now.Add(720 * time.Hour),
	}
	if err := store.CreateRefreshSession(valid); err != nil {
		t.Fatalf("CreateRefreshSession valid: %v", err)
	}

	n, err := store.CleanExpiredRefreshSessions()
	if err != nil {
		t.Fatalf("CleanExpiredRefreshSessions: %v", err)
	}
	if n != 1 {
		t.Errorf("rows affected: got %d, want 1", n)
	}

	// Valid session should still exist.
	got, err := store.GetRefreshSessionByTokenHash("valid-hash")
	if err != nil {
		t.Fatalf("GetRefreshSessionByTokenHash valid: %v", err)
	}
	if got.ID != "valid-rs" {
		t.Errorf("valid session ID: got %q, want %q", got.ID, "valid-rs")
	}

	// Expired session should be gone.
	_, err = store.GetRefreshSessionByTokenHash("expired-hash")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for expired session, got: %v", err)
	}
}

func TestRefreshSessionCascadeDelete(t *testing.T) {
	store := TestStore(t)

	if _, err := store.CreateUser("user-1", "alice"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rs := &RefreshSession{
		ID:        "rs-1",
		UserID:    "user-1",
		TokenHash: "hash-cascade",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	if err := store.CreateRefreshSession(rs); err != nil {
		t.Fatalf("CreateRefreshSession: %v", err)
	}

	// Delete the user; refresh sessions should cascade.
	_, err := store.DB().Exec("DELETE FROM users WHERE id = ?", "user-1")
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}

	_, err = store.GetRefreshSessionByTokenHash("hash-cascade")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after user cascade, got: %v", err)
	}
}

func TestRefreshSessionMissingUser(t *testing.T) {
	store := TestStore(t)

	rs := &RefreshSession{
		ID:        "rs-orphan",
		UserID:    "nonexistent-user",
		TokenHash: "orphan-hash",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	err := store.CreateRefreshSession(rs)
	if err == nil {
		t.Error("expected error for refresh session with missing user, got nil")
	}
}

// --- Index verification ---

func TestSchemaIndexesExist(t *testing.T) {
	store := TestStore(t)

	expectedIndexes := []string{
		"idx_credentials_user_id",
		"idx_challenge_state_user_id",
		"idx_challenge_state_expires_at",
		"idx_refresh_sessions_user_id",
		"idx_refresh_sessions_expires_at",
		"idx_refresh_sessions_token_hash",
	}

	for _, idx := range expectedIndexes {
		var name string
		err := store.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
		).Scan(&name)
		if err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}
}

// --- TestConfig helper ---

func TestTestConfigReturnsValidConfig(t *testing.T) {
	cfg := TestConfig(t)

	if cfg.Port != "0" {
		t.Errorf("Port: got %q, want %q", cfg.Port, "0")
	}
	if cfg.RPID != "localhost" {
		t.Errorf("RPID: got %q, want %q", cfg.RPID, "localhost")
	}
	if cfg.CookieSecure {
		t.Error("CookieSecure should be false in test config")
	}
	if err := cfg.validate(); err != nil {
		t.Errorf("test config should be valid: %v", err)
	}
}

func TestTestStoreIsUsable(t *testing.T) {
	store := TestStore(t)

	// Quick smoke test that the test store works for basic operations.
	user, err := store.CreateUser("test-user", "tester")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != "test-user" {
		t.Errorf("ID: got %q, want %q", user.ID, "test-user")
	}
}

// ======================================================================
// VAL-CROSS-118: Auth restart preserves session data
// ======================================================================

// TestSessionDataSurvivesAuthRestart verifies that auth session data
// persists across a simulated auth service restart. This is the key
// invariant for VAL-CROSS-118: after auth restarts, browser users can
// rehydrate via refresh-token rotation because their session data is
// persisted in SQLite rather than held only in memory.
//
// The test simulates a restart by closing the store and re-opening it
// against the same database file, then verifying that the previously
// created user, credential, and refresh session are still present and
// usable.
func TestSessionDataSurvivesAuthRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "auth-restart-test.db")

	// --- Phase 1: First "run" of auth — create user, credential, refresh session.
	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first OpenStore: %v", err)
	}

	// Create a user.
	user, err := store1.CreateUser("user-restart-001", "restart-tester")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a credential for the user.
	cred := &Credential{
		ID:              "cred-restart-001",
		UserID:          user.ID,
		PublicKey:       []byte("fake-pub-key-for-restart-test"),
		AttestationType: "none",
		Transport:       "[]",
		SignCount:       0,
		AAGUID:          []byte{},
		CreatedAt:       time.Now().UTC(),
	}
	if err := store1.CreateCredential(cred); err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}

	// Create a refresh session for the user.
	rs := &RefreshSession{
		ID:        "rs-restart-001",
		UserID:    user.ID,
		TokenHash: "abc123restart",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	if err := store1.CreateRefreshSession(rs); err != nil {
		t.Fatalf("CreateRefreshSession: %v", err)
	}

	// Close the store (simulate auth shutdown).
	store1.Close()

	// --- Phase 2: Second "run" of auth — reopen the same DB, verify data persists.
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second OpenStore (after restart): %v", err)
	}
	defer store2.Close()

	// Verify the user still exists.
	foundUser, err := store2.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID after restart: %v", err)
	}
	if foundUser.Username != "restart-tester" {
		t.Errorf("user after restart: got username %q, want %q", foundUser.Username, "restart-tester")
	}

	// Verify the credential still exists.
	creds, err := store2.GetCredentialsByUserID(user.ID)
	if err != nil {
		t.Fatalf("GetCredentialsByUserID after restart: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("credentials after restart: got %d, want 1", len(creds))
	}
	if creds[0].ID != "cred-restart-001" {
		t.Errorf("credential ID after restart: got %q, want %q", creds[0].ID, "cred-restart-001")
	}

	// Verify the refresh session still exists and is usable.
	foundRS, err := store2.GetRefreshSessionByTokenHash("abc123restart")
	if err != nil {
		t.Fatalf("GetRefreshSessionByTokenHash after restart: %v", err)
	}
	if foundRS.ID != "rs-restart-001" {
		t.Errorf("refresh session ID after restart: got %q, want %q", foundRS.ID, "rs-restart-001")
	}
	if foundRS.UserID != user.ID {
		t.Errorf("refresh session user after restart: got %q, want %q", foundRS.UserID, user.ID)
	}

	// Verify the refresh session is not expired.
	if time.Now().UTC().After(foundRS.ExpiresAt) {
		t.Error("refresh session expired after restart — should still be valid")
	}
}

// TestRefreshRotationWorksAfterAuthRestart verifies that refresh token
// rotation (the mechanism used for browser rehydration) works correctly
// after a simulated auth restart. This directly exercises the
// VAL-CROSS-118 recovery path: after auth restarts, a browser user's
// expired access JWT is renewed by rotating their refresh token.
func TestRefreshRotationWorksAfterAuthRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "auth-rotation-test.db")

	// --- Phase 1: Create user and initial refresh session.
	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first OpenStore: %v", err)
	}

	user, err := store1.CreateUser("user-rotation-001", "rotation-tester")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rs := &RefreshSession{
		ID:        "rs-rotation-001",
		UserID:    user.ID,
		TokenHash: "initial-token-hash",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	if err := store1.CreateRefreshSession(rs); err != nil {
		t.Fatalf("CreateRefreshSession: %v", err)
	}

	store1.Close()

	// --- Phase 2: After restart, rotate the refresh session.
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second OpenStore (after restart): %v", err)
	}
	defer store2.Close()

	// Look up the existing refresh session (simulating the browser's
	// refresh cookie being presented for renewal).
	foundRS, err := store2.GetRefreshSessionByTokenHash("initial-token-hash")
	if err != nil {
		t.Fatalf("GetRefreshSessionByTokenHash after restart: %v", err)
	}

	// Delete the old session (rotation).
	if err := store2.DeleteRefreshSessionByID(foundRS.ID); err != nil {
		t.Fatalf("DeleteRefreshSessionByID (rotation): %v", err)
	}

	// Create a new rotated session.
	newRS := &RefreshSession{
		ID:        "rs-rotation-002",
		UserID:    user.ID,
		TokenHash: "rotated-token-hash",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}
	if err := store2.CreateRefreshSession(newRS); err != nil {
		t.Fatalf("CreateRefreshSession (rotated): %v", err)
	}

	// Verify the old session is gone.
	_, err = store2.GetRefreshSessionByTokenHash("initial-token-hash")
	if err == nil {
		t.Error("old refresh session should be deleted after rotation")
	}

	// Verify the new session is usable.
	foundNewRS, err := store2.GetRefreshSessionByTokenHash("rotated-token-hash")
	if err != nil {
		t.Fatalf("GetRefreshSessionByTokenHash (rotated): %v", err)
	}
	if foundNewRS.UserID != user.ID {
		t.Errorf("rotated session user: got %q, want %q", foundNewRS.UserID, user.ID)
	}

	// Verify user data is still accessible.
	foundUser, err := store2.GetUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetUserByID after rotation: %v", err)
	}
	if foundUser.Username != "rotation-tester" {
		t.Errorf("user after rotation: got username %q, want %q", foundUser.Username, "rotation-tester")
	}
}
