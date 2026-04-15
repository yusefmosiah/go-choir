# Mission 4: Core Functionality, Security, and Choir in Choir

**Goal:** Fix critical auth issues, harden security, and enable the "choir in choir" vision where the vtext app spawns agents to build features concurrently.

**Philosophy:** Core functionality and security first. Then build "choir in choir" - using the vtext app as a control plane to spawn researchers and coding agents to build more features concurrently in microVMs.

---

## Milestone 1: Auth System Repair

### Problem Statement
- Registration works but login fails
- Currently username-based, should be email-based
- WebAuthn flow needs verification

### Deliverables
1. **Debug and fix login flow**
   - Trace auth flow from Svelte UI → proxy → auth service
   - Identify why login fails while registration succeeds
   - Fix session/cookie handling for login

2. **Migrate to email-based auth**
   - Update database schema (auth.db)
   - Update registration form (email instead of username)
   - Update login form


3. **WebAuthn completion**
   - Verify credential creation during registration
   - Verify credential assertion during login
   - Test browser compatibility (Chrome, Firefox, Safari)
   - Handle WebAuthn errors gracefully

### Verification
- User can register with email
- User can login with email + WebAuthn
- Session persists correctly
- Logout works and clears session

---

## Milestone 2: End-to-End LLM/MAS Validation

### Problem Statement
- Tool calling loop not tested with real providers
- SSE streaming not fully validated
- No agent spawning from vtext app

### Deliverables
1. **Provider integration testing**
   - Bedrock API end-to-end test
   - Z.AI API end-to-end test
   - Tool calling with real function execution
   - Error handling for provider failures

2. **SSE streaming verification**
   - Proxy → runtime SSE stream works
   - Events appear in UI in real-time
   - Reconnection after disconnect
   - Browser compatibility

3. **Etext app → agent spawning**
   - UI button to spawn researcher agent
   - Researcher agent receives context from vtext
   - Agent runs in isolated microVM
   - Results returned to vtext app

### Verification
- Real LLM call with tool execution succeeds
- Streaming events visible in UI
- Agent spawned from vtext completes task
- Failed LLM calls handled gracefully

---

## Milestone 3: Security Hardening

### Problem Statement
- No VM resource limits
- No network isolation
- No rate limiting
- No audit logging

### Deliverables
1. **VM resource constraints**
   - CPU limits (cgroups)
   - Memory limits (cgroups)
   - Disk quota per VM
   - Network bandwidth limits

2. **Network isolation**
   - Firecracker networking setup
   - VM-to-VM isolation
   - VM-to-host isolation (except required APIs)
   - Egress filtering

3. **API security**
   - Rate limiting per user
   - Request size limits
   - Authentication for all /api/* endpoints
   - CSRF protection where needed

4. **Audit logging**
   - Log all VM lifecycle events
   - Log all LLM API calls
   - Log authentication events
   - Structured logs for analysis

### Verification
- VM cannot exceed resource limits
- VM cannot access unauthorized network resources
- API rate limits enforced
- All security events logged

---

## Milestone 4: Choir in Choir (Control Plane)

### Vision
The vtext app becomes a control plane for spawning agents. Users write tasks in vtext, and agents execute them in parallel microVMs.

### Deliverables
1. **Agent orchestration API**
   - POST /api/agent/spawn - create new agent
   - GET /api/agent/:id/status - check agent progress
   - POST /api/agent/:id/cancel - stop agent
   - GET /api/agents - list user's agents

2. **Researcher agent template**
   - Takes research query as input
   - Searches documentation/code
   - Produces summary report
   - Runs in isolated microVM

3. **Coding agent template**
   - Takes feature spec as input
   - Generates implementation plan
   - Writes code in microVM
   - Submits PR or returns patch

4. **Orchestration dashboard**
   - View running agents
   - See agent logs/output
   - Cancel/pause agents
   - Resource usage per agent

### Verification
- Spawn 5 researcher agents in parallel
- Agents complete without interfering
- Dashboard shows real-time status
- Cancelled agents clean up resources

---

## Milestone 5: Iterative Web Desktop Improvement

### Philosophy
Apply GLM-5.1's long-horizon refinement approach: establish framework, then iteratively improve.

### Phase 1: Framework (P0)
- Responsive layout foundation
- Window management system
- Component library

### Phase 2: Mobile Responsive (P1)
- Mobile-optimized shell
- Touch-friendly window controls
- Responsive text editor

### Phase 3: Visual Polish (P2)
- Align with choiros aesthetic
- Consistent theming
- Smooth animations

### Phase 4: Additional Apps (P3)
- File browser
- System monitor
- Calculator
- Terminal

---

## Dependencies

### Must Have Before Milestone 2
- Auth system working (login with email)

### Must Have Before Milestone 4
- End-to-end LLM working (Milestone 2)
- VM security hardened (Milestone 3)

### Can Parallelize
- Web desktop improvements (Milestone 5) can happen alongside other milestones

---

## Success Criteria

1. **Auth:** 100% login success rate, 0 username-based accounts
2. **LLM:** Real Bedrock/Z.AI calls succeed, streaming works
3. **Security:** VMs constrained, network isolated, APIs rate-limited
4. **Choir in Choir:** Can spawn 5+ agents from vtext, monitor progress
5. **Desktop:** Usable on mobile, visually consistent

---

## Reference Materials

- GLM-5.1 blog post on long-horizon generation
- Choiros UI screenshots (user provided)
- Current go-choir UI screenshots (user provided)
- Mission 3 artifacts in `.factory/missions/969491ec-3df3-47c7-b9bf-8e384615819d/`

---

## Open Questions

1. Which LLM provider should be primary for Choir in Choir? (Bedrock vs Z.AI)
2. Should agents share a Dolt database or have isolated storage?
3. How should agent-generated code be reviewed before integration?
4. What are the specific visual design requirements for mobile/desktop?
