# Runtime Invariants

This file captures invariants for agent, Dolt, and VM architecture. These are product constraints, not implementation suggestions.

## Development Tooling Boundary

Choir must not depend on any coding-agent convenience layer.

Development tooling used during this phase must not become a runtime dependency, product concept, user-visible feature, or required repository structure.

## Deployment Source Of Truth

GitHub is the source of truth for tracked files deployed to Node B.

Do not edit or sync git-tracked files directly into `/opt/go-choir` on Node B. Runtime secrets, service environment files, guest images, and generated Nix artifacts may be placed in their designated runtime paths, but source/config changes must land through git and the GitHub Actions deploy flow.

If emergency investigation requires inspecting or patching Node B, revert the checkout to `origin/main` before finishing and make the durable fix locally for review, commit, and deploy.

## Agent Roles

`conductor` receives top-level user and connector input. It routes work to the appagent or flow that should own it. It does not mutate workspace state.

`appagent` means a user-facing app with durable domain state. Appagents mutate their own typed app state through product APIs. They do not get broad shell or arbitrary filesystem mutation by default.

`vtext` is the primary appagent. It is the single writer for canonical document versions. Workers do not write canonical `vtext` text directly; they send findings, artifacts, status, patches, or questions back to `vtext`.

`researcher` is the first dedicated worker role. It reads local context and the web, writes findings/evidence to Dolt, and sends structured handoffs to the owning appagent or super. Researcher is separate because research, evidence, and citation provenance are central to the product.

`super` is the broad execution agent for a microVM. `cosuper` is a durable co-agent under super. For the current phase, high reliance on super/cosuper is acceptable because end-to-end factory operation matters more than perfect least privilege.

Repeated super/cosuper behavior should eventually graduate into narrower tools, workers, or appagents, but only after the end-to-end system works.

## VM Classes

Use three product VM classes.

`active_vm`:

- Every user gets one while actively using the product, including free users.
- Hosts the web desktop runtime, visible app state, appagents, and per-user embedded Dolt.
- Should remain stable and responsive.
- Safe typed app mutations can happen here.

`background_vm`:

- User-owned VM for requested work outside the active desktop.
- Paid-tier feature or quota-controlled trial feature.
- Used for risky mutation, development, package installs, long-running tests, builds, and other work that could destabilize the active desktop.
- Higher tiers may keep these VMs running 24/7.

`shared_worker_vm`:

- Platform-owned pooled VM for cost-efficient background work.
- Used for lower-tier 24/7 work and packaged tasks.
- Must not receive open-ended private workspace access by default.
- Can process sanitized inputs, typed API calls, research jobs, transforms, and artifact generation.

## Super-Tier Execution Policy

Super/cosuper may observe or control the active desktop through typed APIs.

State changes that risk active desktop stability should run in a background VM when possible. Examples include code edits, shell-heavy debugging, dependency installs, test suites with side effects, deploy preparation, and generated workspace changes.

For now, do not over-design locks, file leases, or predeclared edit scopes. The working control points are:

- actor-class tool permissions
- VM placement
- durable mailboxes
- provenance recording
- Trace visibility
- human/appagent review of results

## Durable Mailboxes

Actor messages have a hot-path representation and a durable representation.

For live local actors, the runtime should deliver the message payload directly over an in-memory Go channel or equivalent queue. The actor should not normally need to wake and then query Dolt before it can act.

Dolt-backed mailbox/control records exist for recovery, replay, audit, provenance, and important handoff durability. They are not intended to be the low-latency transport for every message.

```text
append durable control record -> deliver hot-path payload -> process turn -> commit effects/events -> ack durable record
```

The durable record is authoritative when memory and storage disagree: crash, restart, VM resume, backpressure, actor not running, or explicit replay.

Do not mark an important handoff consumed before the actor has committed the result or an explicit failure.

## Cross-VM Routing

Do not use platform Dolt as the network.

Cross-VM routing should use direct transport: HTTP, WebSocket, gRPC, vsock, a lightweight relay, or another low-latency mechanism appropriate to the deployment path.

Platform Dolt stores compact control/provenance facts, not every packet, token, heartbeat, UI event, stream chunk, or internal actor message.

For cross-VM work, the shape should be:

```text
sender runtime -> transport/relay -> receiver runtime
                       |
                       v
             compact durable control record
```

Not:

```text
sender writes platform DB -> receiver polls platform DB -> receiver acts
```

The database is the ledger. It is not the message bus.

## Dolt State

Use per-user embedded Dolt for private desktop/appagent state.

Use platform Dolt for multitenant factory and publication state.

Temporary/background VMs may hold mutable working state, but canonical user/product state must be reported back through durable summaries: artifacts, diffs, findings, versions, publication records, compact lifecycle events, or compute/accounting records.

## Trace

Trace should show trajectories, not isolated loops.

A trajectory starts with user or connector input and continues through conductor routing, appagent ownership, worker delegation, VM execution, findings, artifacts, versions, and publication candidates.

Trace is the debugging and explanation surface for the dark factory. It should make causality visible without forcing the user to read every raw message.
