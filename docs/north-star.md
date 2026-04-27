# Choir North Star

Choir is a multitenant living-document desktop backed by a dark factory of agents and microVMs.

The product is not chat and not a generic coding-agent runner. The first visible unit of value is a living document: a durable, versioned work surface that can accumulate user edits, appagent synthesis, research evidence, execution artifacts, citations, and later publication history.

The long-term system is the Automatic Computer:

1. **Publishing:** private and publishable living documents.
2. **Global memory:** a citation graph connecting published artifacts across users.
3. **Compute economy:** compute accounting, CHIPS, staking, and tokenized ownership in the productive capacity of the network.

Current implementation is Layer 1, but Layer 1 must preserve the facts needed for Layers 2 and 3.

## Current Scope Guard

Do not implement CHIPS yet.

Do not implement wallets, staking, token yield, public citation scoring, or token-denominated compute billing yet.

Do preserve:

- document versions
- user-authored and agent-authored provenance
- worker findings and evidence
- artifact authorship
- citations and citation candidates
- trajectory and causal history
- model/provider/VM attribution
- compute usage per agent turn where available
- publication boundaries between private user state and platform-visible state

## Product Shape

The user sees a web desktop with apps. Apps are backed by appagents.

`vtext` is the primary appagent today. It replaces chat as the control plane. A prompt creates or updates a versioned document. User edits are committed as user-authored versions. Worker messages are synthesized into later appagent-authored versions.

The dark factory is mostly hidden. It contains researchers, supers, cosupers, worker pools, background VMs, shared worker VMs, and platform routing. The factory exists to advance living documents and produce publishable artifacts, not to expose raw orchestration as the main product.

## Dolt Layers

Choir needs both platform Dolt and per-user embedded Dolt.

- **Platform Dolt:** multitenant control-plane and network state: users, VM pool state, routing, worker availability, publication records, shared artifacts, citation graph, compute accounting, and later CHIPS economics.
- **Per-user embedded Dolt:** private user state inside the active desktop VM: desktop/app graph, appagent state, `vtext` versions, prompts, local trajectories, private findings, and unpublished artifacts.
- **Temporary VM state:** forked or hydrated state used for risky development and execution. It reports results back as artifacts, diffs, events, findings, or publication candidates.

Publication is the bridge from private user state to platform-visible network state.

```text
private user Dolt -> publish event -> platform Dolt publication/citation graph
```

Platform Dolt is a ledger, not the hot-path message bus. Cross-VM work should use direct transport or relays for live delivery, while platform Dolt records compact facts needed for routing, recovery, provenance, artifact tracking, publications, citations, and compute accounting.

## Why The Runtime Must Not Collapse

Common LLM simplifications are wrong for Choir:

- Chat history is not canonical state; document versions are.
- A run is not the product unit; a trajectory is the causal path through appagents, workers, evidence, artifacts, and versions.
- Researchers are not optional helpers; evidence and citations are the future economic substrate.
- Worker output should be traceable and potentially citeable later, even when private today.
- VM placement is part of product economics: free users, paid users, background work, 24/7 work, and shared workers all depend on it.

The architecture should optimize for end-to-end productive flow first:

```text
prompt -> conductor -> vtext -> researcher/super -> versions -> publishable artifact -> Trace
```

Then:

```text
published artifacts -> citation graph -> global memory
```

Then:

```text
citation/compute accounting -> CHIPS
```
