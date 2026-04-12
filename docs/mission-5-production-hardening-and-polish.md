# Mission 5: Production Hardening and Polish

**Goal:** Fix production issues on Node B and deploy provider credentials so the multi-agent system works end-to-end.

**Status:** Mission 4 code pushed and CI passing. Need to deploy to Node B and verify.

---

## Critical Production Issues (Priority 1)

### Issue 1: Auth Database Schema - DEPLOYED, NEEDS VERIFY
**Status:** Fix pushed in commit `790aab9`, CI passing.

**Problem:** The deployed auth database on Node B still has old schema with `username TEXT UNIQUE NOT NULL` column.

**Solution:** The code now has proper migration to drop username column. Need to:
1. SSH to Node B
2. Either delete `/var/lib/go-choir/auth/auth.db` (clean slate) or let migration run
3. Restart auth service
4. Verify registration works with email + passkey

### Issue 2: Provider Credentials Not Deployed
**Problem:** Gateway doesn't have Fireworks or Z.AI API keys. LLM calls fail on production.

**Fix:**
1. Add secrets to Node B: `/var/lib/go-choir/secrets/fireworks-api-key` and `zai-api-key`
2. Update `nix/node-b.nix` to inject keys as environment variables
3. Rebuild and restart gateway service
4. Verify LLM calls work

---

## Post-Mission 5: UX Rewrite (Mission 6)

**Note:** The current UX has fundamental issues that require a full rewrite:
- Wrong desktop paradigm (top bar instead of desktop icons)
- E-text has wrong UX (research button, sidebar)
- Missing prompt bar (conductor)
- Not responsive for mobile

**Mission 6** will be a complete desktop UX rewrite following the ChoirOS pattern:
- Desktop icons on left rail
- Floating windows
- Prompt bar at bottom (conductor)
- Simple e-text editor
- Responsive for mobile

See: `docs/mission-6-desktop-ux-rewrite.md`

---

## Mission 5 Milestones

### Milestone 1: Auth Fix Deploy (DONE → VERIFY)
**Status:** Code pushed, CI passing. Need to verify on Node B.

**Steps:**
1. SSH to Node B (147.135.70.196)
2. Delete old auth.db: `rm /var/lib/go-choir/auth/auth.db` (clean slate - no real users)
3. `nixos-rebuild switch` to deploy latest
4. Verify auth service restarts clean
5. Test: Register with email + passkey on https://draft.choir-ip.com

**Validation:**
- Registration succeeds
- Login with passkey works
- Re-login works (the original bug is fixed)

### Milestone 2: Provider Credentials Deploy
**Status:** Not started.

**Steps:**
1. Create secrets files on Node B:
   - `/var/lib/go-choir/secrets/fireworks-api-key`
   - `/var/lib/go-choir/secrets/zai-api-key`
2. Update `nix/node-b.nix` to add keys to gateway Environment
3. `nixos-rebuild switch`
4. Restart gateway service
5. Test: LLM call through gateway works

**Validation:**
- `curl https://draft.choir-ip.com/provider/v1/health` returns 200 with provider count
- Submit etext prompt, receive streaming LLM response

### Milestone 3: End-to-End Verification
**Full flow test on production:**
1. Register/login on draft.choir-ip.com
2. Open etext, create document
3. Type in prompt bar (conductor) - request research
4. See worker spawn and complete
5. Results appear in etext

**Note:** UX will be clunky (research button, wrong desktop pattern) but functionality should work. Mission 6 will fix the UX.

---

## Technical Details

### Auth Schema Fix

The migration in `internal/auth/store.go` needs to:

1. For new databases:
   ```sql
   CREATE TABLE users (
       id         TEXT PRIMARY KEY,
       email      TEXT UNIQUE NOT NULL,  -- NOT username
       created_at DATETIME NOT NULL
   );
   ```

2. For existing databases (Node B fix):
   ```sql
   -- Migrate existing users
   UPDATE users SET email = username WHERE email IS NULL;
   
   -- Drop username, make email required
   -- (SQLite doesn't support DROP COLUMN directly, need recreate)
   ```

### Provider Credentials

Add to `nix/node-b.nix`:
```nix
Environment = [
  "GATEWAY_PORT=8084"
  "FIREWORKS_API_KEY_FILE=/var/lib/go-choir/secrets/fireworks-api-key"
  "ZAI_API_KEY_FILE=/var/lib/go-choir/secrets/zai-api-key"
];
```

---

## Success Criteria

1. **Auth:** New user can register with email + passkey on draft.choir-ip.com
2. **LLM:** Etext prompt produces streaming LLM response
3. **Choir:** Research button spawns worker and returns results
4. **UX:** Polish issues resolved through user testing
5. **Docs:** Clear setup/runbook for operators

---

## References

- Mission 4 completion: `docs/mission-4-core-functionality-and-choir-in-choir.md`
- Node B config: `nix/node-b.nix`
- Auth store: `internal/auth/store.go`
- Current issues: CI failures, deployed UX problems
