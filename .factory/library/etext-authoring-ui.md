# E-Text Authoring and History UI

This document describes the e-text authoring and history UI implemented for the etext-desktop milestone.

## Frontend Components

### ETextEditor.svelte
The main e-text document editing component, rendered inside the desktop window when the user launches the E-Text app. Supports:

- **Document list view** — Shows all documents owned by the authenticated user
- **Document creation** — Create new documents with durable identity (VAL-ETEXT-001)
- **Editor view** — Textarea for direct content editing with save button
- **Revision saving** — Direct user edits create canonical user-authored revisions (VAL-ETEXT-002)
- **Citations panel** — JSON editor for citations array (round-trips through history, VAL-ETEXT-010)
- **Metadata panel** — JSON editor for metadata object (round-trips through history, VAL-ETEXT-010)
- **History view** — Chronological revision list with author kind/label attribution (VAL-ETEXT-006)
- **Snapshot view** — View historical revision without mutating head (VAL-ETEXT-007)
- **Diff view** — Compare two revisions showing added/removed/unchanged sections (VAL-ETEXT-008)
- **Blame view** — Section-level attribution showing user vs agent authorship (VAL-ETEXT-009)

### etext.js
API client for all e-text endpoints. Uses `fetchWithRenewal` for cookie-backed same-origin auth.

### Desktop.svelte integration
The e-text app content in the desktop window was replaced from a placeholder hint ("Document editing will be available in a later feature") to the real `ETextEditor` component. The `data-etext-app` attribute is on the wrapper div for test targeting.

## Proxy Routing

The proxy handler (`internal/proxy/handlers.go`) was updated to route `/api/etext/*` requests through `HandleProtectedAPI`, which auth-gates at the proxy level and forwards to the sandbox with `X-Authenticated-User` injected. This matches the pattern used by `/api/desktop/state` and runtime API routes.

## Data Attributes for Test Targeting

- `[data-etext-editor]` — root editor container
- `[data-etext-doclist]` — document list panel
- `[data-etext-docitem]` — individual document in the list
- `[data-etext-newdoc]` — new document button
- `[data-etext-newdoc-title]` — new document title input
- `[data-etext-newdoc-submit]` — new document submit button
- `[data-etext-editor-area]` — text editing textarea
- `[data-etext-save]` — save button
- `[data-etext-title]` — document title display
- `[data-etext-citations]` — citations section
- `[data-etext-metadata]` — metadata section
- `[data-etext-history-btn]` — button to open history view
- `[data-etext-history]` — history entries container
- `[data-etext-history-entry]` — single history entry
- `[data-etext-history-author-kind]` — author kind attribute on history entry
- `[data-etext-snapshot-content]` — snapshot content display
- `[data-etext-snapshot-citations]` — snapshot citations section
- `[data-etext-snapshot-metadata]` — snapshot metadata section
- `[data-etext-diff-stats]` — diff statistics (+/- line counts)
- `[data-etext-diff-sections]` — diff sections container
- `[data-etext-blame-sections]` — blame sections container
- `[data-etext-blame-section]` — single blame section
- `[data-etext-blame-author-kind]` — author kind attribute on blame section
- `[data-etext-app]` — wrapper div inside desktop window content

## Playwright Test Coverage

9 tests in `frontend/tests/etext-authoring-history.spec.js` covering all 8 validation assertions:
- VAL-ETEXT-001: User can create a document
- VAL-ETEXT-002: Direct user edits create canonical user-authored revisions
- VAL-ETEXT-005: Latest revision survives reload and fresh login session
- VAL-ETEXT-006: Version history lists revisions with explicit attribution
- VAL-ETEXT-007: Historical snapshots can be opened without mutating head
- VAL-ETEXT-008: Diff view compares selected revisions
- VAL-ETEXT-009: Blame identifies the last editor per section
- VAL-ETEXT-010: Citations and metadata persist with document history

## Persistence Behavior

Document and revision state are persisted server-side in the sandbox's SQLite store (same database as runtime state). This means:
- Latest saved content survives browser reload (proven by Playwright test)
- Latest saved content survives logout and fresh login for the same user (proven by Playwright test)
- Desktop window state is persisted separately via `/api/desktop/state`
- After reload, the desktop restores the e-text window, and the ETextEditor shows the document list where the user can reopen their document

## Future Work

- **Appagent revision flow**: The next feature (etext-appagent-revision-and-attribution) will add prompt-driven canonical appagent revisions with live progress
- **In-document revision progress**: While an agent revision runs, the document should show progress without manual refresh
- **Delta-based revisions**: For very large documents, delta-based storage could be added but current full-snapshot approach is simpler
