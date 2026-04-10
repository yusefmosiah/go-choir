# Auth Package

This document describes the `internal/auth` package built across auth-foundation features.

## Package Structure

- `internal/auth/config.go` — `Config` struct and `LoadConfig()` function that resolves all AUTH_* env vars
- `internal/auth/store.go` — `Store` struct wrapping SQLite with schema bootstrap and CRUD operations
- `internal/auth/handlers.go` — HTTP handlers for `/auth/register/begin`, `/auth/login/begin`, and `/auth/session`
- `internal/auth/webauthn_user.go` — Adapter implementing `webauthn.User` interface for Store User + Credentials
- `internal/auth/keys.go` — Ed25519 private key loading from OpenSSH PEM files
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

`cmd/auth/main.go` loads config, ensures dirs, opens the store, creates the WebAuthn instance, loads the Ed25519 signing key, creates a `Handler`, and registers `/auth/register/begin`, `/auth/login/begin`, and `/auth/session` routes on the shared server.

## Auth HTTP Handlers

### Handler construction

`auth.NewHandler(store, wa, cfg, signer)` creates a `Handler` with:
- `store` — SQLite Store for user/credential/challenge persistence
- `wa` — `*webauthn.WebAuthn` instance bound to the configured RP ID and origins
- `cfg` — `*Config` for cookie settings and RP ID
- `signer` — `ed25519.PrivateKey` for JWT signing/verification

### POST /auth/register/begin

Accepts `{"username": "alice"}`, creates the user if not existing, generates WebAuthn registration options with a challenge, persists the challenge in `challenge_state`, and returns `*protocol.CredentialCreation` as JSON. Malformed or missing input returns JSON 4xx.

### POST /auth/login/begin

Accepts `{"username": "alice"}`, looks up existing user and credentials, generates WebAuthn assertion options with a challenge, persists the challenge in `challenge_state`, and returns `*protocol.CredentialAssertion` as JSON. Returns 404 for unknown users or users without passkeys. Malformed input returns JSON 4xx.

### GET /auth/session

Returns `{"authenticated": false}` for missing, empty, bogus, expired, or tampered access JWT cookies. Returns `{"authenticated": true, "user": {...}}` for valid JWTs. Never returns 5xx for invalid auth state. Never leaks tokens, passkey material, or challenge records.

### Key loading

`auth.LoadPrivateKey(path)` reads an OpenSSH PEM-encoded Ed25519 private key and returns `ed25519.PrivateKey`. The init.sh script generates keys in this format.

### Test helpers for handler tests

- `testHandlerEnv(t)` — creates a Handler with test Store, WebAuthn, and temporary Ed25519 key material
- `writeTestKey(t, path, priv)` — writes an Ed25519 private key in OpenSSH PEM format
