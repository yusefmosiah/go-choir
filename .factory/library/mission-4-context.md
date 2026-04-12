# Mission 4 Context

**Mission:** Auth Fix, LLM Validation, and Minimal Choir-in-Choir
**Mission Dir:** `/Users/wiz/.factory/missions/0411ccd8-943c-4cab-92ab-9528cccd79b5`
**Date:** 2026-04-12

## The Auth Re-Login Bug (Critical Priority)

### Symptoms
- Users can register a passkey successfully
- Users can login immediately after registration
- **Users CANNOT re-login later with the same passkey** (THE BUG)
- Same username can register multiple times (symptom of incomplete registration)

### Root Cause Hypotheses
1. **Sign counter not updated** - WebAuthn requires sign counter to be stored and updated after each login. If not updated, re-login verification may fail.
2. **Challenge lookup failure** - Challenge state may be looked up incorrectly on second login attempt.
3. **Credential ID mismatch** - Credential ID encoding/decoding may differ between registration and login.

### Fix Approach
Check these locations:
- `internal/auth/handlers.go:HandleLoginFinish()` - verify sign counter update
- `internal/auth/store.go:UpdateCredentialSignCount()` - ensure this is called
- Database: check `credentials` table has `sign_count` column that's being updated

### Files to Modify
- `internal/auth/handlers.go` - ensure sign counter updated in transaction
- `internal/auth/store.go` - verify sign counter methods work
- Tests: add re-login test case in `handlers_test.go`

## Email Migration (Username → Email)

### Scope
- Change primary identifier from `username` to `email`
- Update database schema: add `email` column with UNIQUE constraint
- Update handlers: accept `email` field in request bodies
- Update frontend: email input instead of username
- WebAuthn display name should use email

### Migration Strategy
Existing users: Need to decide approach
1. **Option A:** Add email as optional, require for new registrations, migrate existing gradually
2. **Option B:** Backfill existing users with placeholder emails, require update on next login
3. **Option C:** Reset database (if acceptable for development)

Default: Option A (least disruptive)

## Minimal Choir-in-Choir (Not Full Architecture)

### What This IS
- Simple work registry table to track spawned tasks
- Parent-child task linking via `parent_id` field
- POST /api/agent/spawn API
- Etext "Research" button that spawns a worker
- Channel-based result delivery (using existing ChannelManager)

### What This is NOT (Deferred)
- Full Conductor/Scheduler architecture from architecture.md
- Dolt migration (SQLite remains)
- Cross-app work registry (etext-only)
- VM isolation for workers (goroutines in same sandbox)
- Full attestation model
- Multi-model routing
- Worker pools and scheduling algorithms

### Key Design Decisions
- Workers are goroutines in the same sandbox (not separate VMs)
- Simple SQLite table for work registry (not full work graph)
- Parent receives results via ChannelManager (not polling)
- Etext appagent spawns workers directly (not through scheduler service)

## Provider Priority

### Primary (for testing)
1. **Fireworks AI** - Fast, cheap, good for testing
2. **Z.AI (GLM-5-turbo)** - Fast, cheap, good for testing

### Production (expensive, use sparingly)
- **AWS Bedrock (Claude)** - Capable but expensive, save for production

## Node B Deployment

- **URL:** https://draft.choir-ip.com
- **IP:** 147.135.70.196
- **SSH:** `ssh root@147.135.70.196` (credentials in user's private notes)
- **Services:** auth (8081), proxy (8082), gateway (8084), vmctl (8083), sandbox (8085 in VMs)
- **Caddy:** Public edge on 80/443

### Deploy Process
1. GitHub Actions builds on push to main
2. SSH to Node B
3. `git pull && nix build && nixos-rebuild switch`
4. Verify health endpoints

## Testing Strategy

### Local Development
```bash
# Start services
source start-services.sh

# Test auth
curl -s http://localhost:8081/auth/register/begin -d '{"email":"test@example.com"}'

# Test LLM
curl -s http://localhost:8084/provider/v1/inference -d '{...}'
```

### Node B Validation
```bash
# Health checks
curl https://draft.choir-ip.com/auth/session
curl https://draft.choir-ip.com/api/health  # requires auth

# Full flow via agent-browser
# (use agent-browser skill for browser flows)
```

## Mission Boundaries

**In Scope:**
- Auth re-login bug fix
- Email-based auth migration
- Fireworks + Z.AI provider integration
- SSE streaming verification
- Tool calling with at least one tool
- Minimal scheduler (work registry + spawn API)
- Etext worker spawning

**Out of Scope (Deferred):**
- Dolt migration
- Full Conductor/Scheduler/AppAgent model
- VM isolation for workers
- Desktop polish/responsive design
- Bedrock provider (testing only)
- Multi-user collaboration
- Advanced orchestration patterns

## References

- Full architecture vision: `docs/architecture.md` (for context, not implementation target)
- Mission 4 proposal: `/Users/wiz/.factory/missions/0411ccd8-943c-4cab-92ab-9528cccd79b5/mission.md`
- Validation contract: `/Users/wiz/.factory/missions/0411ccd8-943c-4cab-92ab-9528cccd79b5/validation-contract.md`
- Features: `/Users/wiz/.factory/missions/0411ccd8-943c-4cab-92ab-9528cccd79b5/features.json`
