# E-Text Research Button (Choir-in-Choir)

**Feature:** etext-research-button (Milestone: choir-in-choir)
**Date:** 2026-04-12

## Overview

The Research button in the etext editor toolbar enables users to spawn a background worker task that researches the document topic. This is the first "choir-in-choir" UI feature where the etext app spawns worker agents.

## Architecture

### Flow

1. User clicks "🔍 Research" button in etext editor toolbar
2. Frontend calls `spawnResearchWorker(docId, content, topic)` which submits a task via `POST /api/agent/task`
3. Task runs in background (non-blocking, user can continue editing)
4. Frontend polls `/api/agent/status` for progress
5. On completion, research results appear in a panel below the toolbar
6. User can "Apply to Document" (inserts results) or "Dismiss"

### Key Design Decisions

- Uses `POST /api/agent/task` (not `POST /api/agent/spawn`) because the research worker is a standalone task — it doesn't need a parent task reference
- Task metadata includes `{type: "etext_research", doc_id: docId}` for context passing (VAL-CHOIR-013)
- The prompt includes the full document content as context for the LLM
- Results are shown in a panel — user explicitly chooses to apply them to the document

### Frontend Components

| File | Purpose |
|------|---------|
| `frontend/src/lib/etext.js` | `spawnResearchWorker()`, `fetchResearchStatus()` API functions |
| `frontend/src/lib/ETextEditor.svelte` | Research button UI, progress indicator, results panel |
| `frontend/tests/etext-research-button.spec.js` | 9 Playwright tests |

### Data Attributes (for testing)

| Attribute | Element |
|-----------|---------|
| `data-etext-research` | Research button in toolbar |
| `data-etext-research-progress` | Progress indicator while running |
| `data-etext-research-result` | Results panel when completed |
| `data-etext-research-result-content` | Results text content |
| `data-etext-research-apply` | Apply results to document button |
| `data-etext-research-dismiss` | Dismiss results button |

### State Machine

```
'' → pending → running → completed (results shown)
                        → failed (error shown)
```

## Validation Assertions Fulfilled

- **VAL-CHOIR-007**: Research button visible, spawns worker, progress shown, results appear
- **VAL-CHOIR-012**: Rapid resubmission handled (button disabled while running)
- **VAL-CHOIR-013**: Task metadata includes doc_id for context passing
