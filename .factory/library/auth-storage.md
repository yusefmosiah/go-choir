# Auth Storage Foundation

This document describes the `internal/auth` package created during the `auth-storage-foundation` feature.

## Package Structure

- `internal/auth/config.go` — `Config` struct and `LoadConfig()` function that resolves all AUTH_* env vars
- `internal/auth/store.go` — `Store` struct wrapping SQLite with schema bootstrap and CRUD operations
- `internal/auth/testhelpers.go` — `TestStore(t)` and `TestConfig(t)` helpers for unit tests

## Configuration (AUTH_* env vars)

All config is loaded from environment variables. No secrets are hardcoded.

| Variable | Default | Description |
|---|---|---|
| `AUTH_PORT` | `8081` | Listen port |
| `AUTH_DB_PATH` | `/tmp/go-choir-m2/auth/auth.db` | SQLite database path |
| `AUTH_RP_ID` | `localhost` | WebAuthn RP ID |
| `AUTH_RP_ORIGINS` | `http://localhost:4173` | Comma-separated WebAuthn origins |
| `AUTH_JWT_PRIVATE_KEY_PATH` | `/tmp/go-choir-m2/auth-signing-key` | Ed25519 private key path |
| `AUTH_ACCESS_TOKEN_TTL` | `5m` | Short-lived access JWT TTL |
| `AUTH_REFRESH_TOKEN_TTL` | `720h` | Refresh token TTL |
| `AUTH_COOKIE_SECURE` | `false` | Set Secure flag on auth cookies |

When explicit path env vars are omitted, defaults resolve under `/tmp/go-choir-m2`. The `init.sh` script creates this directory and generates a test Ed25519 key.

## SQLite Schema

Four tables with foreign-key cascades:

- **users** — `id` (TEXT PK), `username` (TEXT UNIQUE), `created_at`
- **credentials** — `id` (TEXT PK), `user_id` (FK→users), `public_key`, `attestation_type`, `transport`, `sign_count`, `aaguid`, `created_at`
- **challenge_state** — `id` (TEXT PK), `user_id` (FK→users), `challenge`, `type` (CHECK: 'registration'|'login'), `allowed_credentials`, `created_at`, `expires_at`
- **refresh_sessions** — `id` (TEXT PK), `user_id` (FK→users), `token_hash`, `created_at`, `expires_at`, `rotated_from`

Indexes cover `user_id`, `expires_at`, and `token_hash` lookups.

WAL mode and foreign keys are enabled on every connection.

## Store API

The `Store` provides CRUD for all four tables:

- User: `CreateUser`, `GetUserByID`, `GetUserByUsername`
- Credential: `CreateCredential`, `GetCredentialsByUserID`, `UpdateCredentialSignCount`
- ChallengeState: `SaveChallengeState`, `GetChallengeStateByID`, `DeleteChallengeStateByID`, `CleanExpiredChallenges`
- RefreshSession: `CreateRefreshSession`, `GetRefreshSessionByTokenHash`, `DeleteRefreshSessionByID`, `DeleteRefreshSessionsByUserID`, `CleanExpiredRefreshSessions`

## Test Helpers

- `auth.TestStore(t)` — opens a temp SQLite database and bootstraps the schema; auto-closes on test cleanup
- `auth.TestConfig(t)` — returns a valid Config with temp paths suitable for unit testing

## cmd/auth Integration

`cmd/auth/main.go` loads config via `auth.LoadConfig()`, ensures dirs, opens the store, and starts the HTTP server. The store and config are ready for handler wiring in later features.
