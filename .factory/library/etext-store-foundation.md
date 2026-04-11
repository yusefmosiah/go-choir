# E-Text Store and History/Diff/Blame Foundation

This document describes the e-text backend foundations implemented for the etext-desktop milestone.

## Data Model

### AuthorKind

`AuthorKind` distinguishes who created a canonical revision:
- `user` — direct user edits create canonical user-authored revisions
- `appagent` — appagent actions create canonical appagent-authored revisions
- Subordinate workers **never** directly become canonical authors (core system invariant)

### Document

The `Document` type carries identity (DocID), ownership (OwnerID), title, and a pointer to the current head revision (CurrentRevisionID). Document records are owner-scoped: all queries require ownerID matching.

### Revision

Revisions are **immutable** full-content snapshots. Each revision carries:
- Stable revision ID
- Full document text (not deltas — enables direct snapshot access)
- Citations (JSON array) and metadata (JSON object) — persisted per-revision for history round-trips
- Authorship: AuthorKind + AuthorLabel for attribution
- Parent chain: ParentRevisionID links to the previous revision

### Citations and Metadata

Citations (`[]Citation`) and metadata (`map[string]any`) are stored as JSON blobs per-revision. This ensures they round-trip through history and remain available when inspecting historical revisions, not only the current head (VAL-ETEXT-010).

## Store Layer

### Persistence

- SQLite with WAL mode, matching the existing auth/runtime store pattern
- `SetMaxOpenConns(1)` for SQLite safety
- E-text schema is auto-bootstrapped alongside the runtime schema in the same database
- Separate `OpenEtextWorkspace()` function available for per-user workspace databases

### Tables

- `etext_documents` — document records with owner scoping
- `etext_revisions` — revision records with doc/owner indexes and created_at ordering

### Owner Scoping

All store queries are owner-scoped. One user cannot read another user's documents, revisions, history, diffs, or blame results. This supports the multi-user isolation invariant.

## API Endpoints

All e-text APIs are behind cookie-backed same-origin auth (X-Authenticated-User header injected by proxy):

| Method | Path | Description |
|--------|------|-------------|
| POST | /api/etext/documents | Create document |
| GET | /api/etext/documents | List documents |
| GET | /api/etext/documents/{id} | Get document |
| PUT | /api/etext/documents/{id} | Update document |
| DELETE | /api/etext/documents/{id} | Delete document |
| POST | /api/etext/documents/{id}/revisions | Create revision |
| GET | /api/etext/documents/{id}/revisions | List revisions |
| GET | /api/etext/documents/{id}/history | Revision history |
| GET | /api/etext/revisions/{id} | Get revision (snapshot) |
| GET | /api/etext/revisions/{id}/blame | Blame revision |
| GET | /api/etext/diff?from=X&to=Y | Diff two revisions |

### Revision Creation

When a revision is created:
1. The revision record is inserted into `etext_revisions`
2. The document's `current_revision_id` is automatically updated to the new revision
3. If `parent_revision_id` is not specified, it defaults to the document's current head

### Author Kind Validation

The API rejects invalid `author_kind` values. Only `user` and `appagent` are accepted. This enforces the invariant that subordinate workers never directly become canonical authors.

## Diff Algorithm

The diff is computed using a longest common subsequence (LCS) algorithm on lines. It produces `DiffSection` values classifying each region as:
- `unchanged` — matching lines between revisions
- `added` — lines present in `to` but not `from`
- `removed` — lines present in `from` but not `to`

Adjacent sections of the same type are merged for readability.

## Blame Algorithm

The blame algorithm walks backward through the revision chain from the head to the root, attributing each line in the head revision to the most recent revision that changed it. It produces `BlameSection` values with per-section attribution showing:
- Which revision last modified the section
- Whether the author was user or appagent
- The author label
- The section's line range and content

## Routing

E-text routes are registered as:
- Exact match: `/api/etext/documents` for create/list
- Prefix match: `/api/etext/` dispatched by `HandleEtextRouter` which inspects URL path segments to route to the appropriate handler

This avoids ambiguity with Go's `http.ServeMux` prefix matching behavior.

## Future Work

- **Appagent revision flow**: The next feature will add runtime-driven canonical appagent revisions with live progress
- **Dolt migration**: The current SQLite store is designed to migrate toward Dolt-backed per-user workspaces when Dolt integration is ready
- **Delta-based revisions**: For very large documents, delta-based storage could reduce storage, but full-content snapshots are simpler and more robust for history access
