# E-Text Appagent Revision and Attribution

## Feature: etext-appagent-revision-and-attribution

This feature implements the appagent revision flow for the e-text desktop milestone.

### Assertions Fulfilled
- VAL-ETEXT-003: Agent revision creates canonical appagent-authored revisions
- VAL-ETEXT-004: Revision progress and completion update the open document without manual refresh
- VAL-CROSS-119: One end-to-end e-text flow preserves user and appagent attribution
- VAL-CROSS-120: Background worker output never appears as a direct canonical author
- VAL-CROSS-122: Renewal and retries do not duplicate canonical document mutation

### Architecture

#### Agent Revision Flow
1. User submits a natural-language prompt via `POST /api/etext/documents/{id}/agent-revision`
2. The handler checks for an existing pending agent mutation (idempotency)
3. If no pending mutation exists, it creates a runtime task with `type=etext_agent_revision` metadata
4. The task executes through the normal runtime provider path
5. On completion, `handleTaskCompletion` creates a canonical `author_kind=appagent` revision
6. The mutation is marked as completed, preventing duplicate revisions

#### Idempotency (VAL-CROSS-122)
- The `etext_agent_mutations` table tracks in-flight mutations by (doc_id, task_id)
- `GetPendingAgentMutationByDoc` returns the existing task if a mutation is already in-flight
- `CompleteAgentMutation` uses `WHERE state = 'pending'` to prevent double-completion
- `ErrMutationAlreadyCompleted` signals that the revision was already created

#### Event Vocabulary
- `etext.agent_revision.started` — emitted when the task is submitted (carries doc_id, task_id)
- `etext.agent_revision.progress` — emitted during task execution (carries doc_id, phase)
- `etext.agent_revision.completed` — emitted when the canonical revision is created (carries doc_id, revision_id, task_id)
- `etext.agent_revision.failed` — emitted on task failure (carries doc_id, error)

#### Frontend
- `submitAgentRevision(docId, prompt)` in etext.js submits the agent revision via fetchWithRenewal
- ETextEditor.svelte has an agent prompt input bar with progress/completion indicators
- Polling via `/api/agent/status` updates the document content on completion

### Key Design Decisions
- Users and appagents are peer canonical editors — only `user` and `appagent` are valid author_kind values
- Subordinate workers never become canonical authors (enforced by author_kind validation)
- Agent revision tasks include the full document content in the prompt so the provider can revise it
- The mutation table is per-document and per-task, preventing duplicate mutations even across auth renewal/retry
