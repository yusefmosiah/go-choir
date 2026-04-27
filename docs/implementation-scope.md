# Implementation Scope

Current implementation target: make Layer 1 work end to end while preserving the data needed for publication, citations, and later compute economics.

## Layer 1 Target

The near-term product loop is:

```text
prompt -> conductor -> vtext -> researcher/super -> document versions -> Trace
```

This means:

- prompt-bar and connector input route through conductor
- `vtext` owns the living document state
- user edits become user-authored versions and diffs
- `vtext` writes appagent-authored versions
- researchers produce durable evidence and findings
- super/cosupers handle broad execution work when needed
- risky mutation happens in background VMs when available
- Trace explains the trajectory

## Current Non-Goals

Do not implement:

- CHIPS token mechanics
- wallets
- staking
- public citation scoring
- token-denominated billing
- decentralized inference markets
- automated carry/revenue accounting

These concepts explain why the architecture must preserve provenance, citations, artifacts, and compute usage, but they are not current product code.

## Data To Capture Now

Capture these facts even before publication and CHIPS exist:

- actor ID and role for each meaningful action
- VM ID, VM class, and epoch where work ran
- model/provider where available
- compute usage where available
- input message and output event linkage
- document version authorship
- findings and evidence provenance
- artifact producer and source trajectory
- citation candidates
- publication/private boundary

## Simplification Rules For Agents

Future coding agents must not simplify Choir into:

- chat plus task runner
- one global agent with tools
- parent/child runs as the main architecture
- SQLite-only runtime truth
- single-VM-only assumptions
- local-development-only behavior
- platform-Dolt-as-global-message-bus designs

If a change touches runtime, `vtext`, Trace, Dolt, `vmctl`, worker tools, or appagent behavior, read:

- `docs/north-star.md`
- `docs/runtime-invariants.md`
- `PROJECT-GOALS.md`
- `PROJECT-GLOSSARY.md`

## Build Order

1. Make the current `vtext` living-document loop reliable.
2. Make researcher and super/cosuper work visible and trustworthy in Trace.
3. Move runtime truth toward hot-path actor delivery plus durable handoff records and Dolt-backed product state.
4. Use `vmctl` as factory capacity management, not just deployment plumbing.
5. Add publication and citation graph mechanics after enough living-document content exists.
6. Add CHIPS/accounting mechanics only after publication/citation data exists.
