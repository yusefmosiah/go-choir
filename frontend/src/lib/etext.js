/**
 * E-Text API client for the go-choir desktop shell.
 *
 * Communicates with the e-text document/revision/history/diff/blame APIs
 * through the same-origin proxy routes only:
 *   POST   /api/etext/documents           — create a new document
 *   GET    /api/etext/documents           — list documents
 *   GET    /api/etext/documents/{id}      — get a document
 *   PUT    /api/etext/documents/{id}      — update a document (title)
 *   DELETE /api/etext/documents/{id}      — delete a document
 *   POST   /api/etext/documents/{id}/revisions — create a revision
 *   GET    /api/etext/documents/{id}/revisions — list revisions
 *   GET    /api/etext/revisions/{id}     — get a revision (snapshot)
 *   GET    /api/etext/documents/{id}/history — revision history
 *   GET    /api/etext/diff?from=X&to=Y    — diff two revisions
 *   GET    /api/etext/revisions/{id}/blame — blame revision
 *   POST   /api/etext/documents/{id}/agent-revision — submit agent revision
 *   POST   /api/agent/task                            — spawn a worker task (research)
 *   GET    /api/agent/status                          — poll worker task status
 *
 * All requests use cookie-backed auth via fetchWithRenewal so that:
 *   - expired access tokens are silently renewed before retry
 *   - the e-text UI never falls back to guest auth mid-operation
 *   - renewal/retry does not duplicate document mutations (VAL-CROSS-122)
 *
 * Document and revision state are persisted server-side so they survive
 * reload and fresh login for the same user (VAL-ETEXT-005).
 */

import { fetchWithRenewal, AuthRequiredError } from './auth.js';

// ---------------------------------------------------------------------------
// Document CRUD
// ---------------------------------------------------------------------------

/**
 * Creates a new e-text document.
 *
 * Returns a durable document identity (VAL-ETEXT-001).
 *
 * @param {string} title - The document title.
 * @returns {Promise<{doc_id: string, owner_id: string, title: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function createDocument(title) {
  const res = await fetchWithRenewal('/api/etext/documents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Create document failed (${res.status})`);
  }

  return res.json();
}

/**
 * Lists documents owned by the authenticated user.
 *
 * @returns {Promise<{documents: Array<{doc_id: string, owner_id: string, title: string, current_revision_id: string, created_at: string, updated_at: string}>}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function listDocuments() {
  const res = await fetchWithRenewal('/api/etext/documents', {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `List documents failed (${res.status})`);
  }

  return res.json();
}

/**
 * Gets a specific document by ID.
 *
 * @param {string} docId - The document ID.
 * @returns {Promise<{doc_id: string, owner_id: string, title: string, current_revision_id: string, created_at: string, updated_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function getDocument(docId) {
  const res = await fetchWithRenewal(`/api/etext/documents/${encodeURIComponent(docId)}`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Get document failed (${res.status})`);
  }

  return res.json();
}

/**
 * Updates a document (e.g., title).
 *
 * @param {string} docId - The document ID.
 * @param {string} title - The new title.
 * @returns {Promise<{doc_id: string, owner_id: string, title: string, current_revision_id: string, created_at: string, updated_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function updateDocument(docId, title) {
  const res = await fetchWithRenewal(`/api/etext/documents/${encodeURIComponent(docId)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Update document failed (${res.status})`);
  }

  return res.json();
}

/**
 * Deletes a document and its revisions.
 *
 * @param {string} docId - The document ID.
 * @returns {Promise<{ok: boolean}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function deleteDocument(docId) {
  const res = await fetchWithRenewal(`/api/etext/documents/${encodeURIComponent(docId)}`, {
    method: 'DELETE',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Delete document failed (${res.status})`);
  }

  return res.json();
}

// ---------------------------------------------------------------------------
// Revisions
// ---------------------------------------------------------------------------

/**
 * Creates a new revision for a document.
 *
 * Saving direct edits creates a canonical revision with stable revision
 * identifier attributable to the signed-in user (VAL-ETEXT-002).
 *
 * @param {string} docId - The document ID.
 * @param {object} options
 * @param {string} options.content - The document content text.
 * @param {'user'|'appagent'} options.authorKind - Who is creating the revision.
 * @param {string} options.authorLabel - Human-readable label for the author.
 * @param {Array} [options.citations] - Citations array.
 * @param {object} [options.metadata] - Metadata object.
 * @param {string} [options.parentRevisionId] - Parent revision ID (defaults to document head).
 * @returns {Promise<{revision_id: string, doc_id: string, owner_id: string, author_kind: string, author_label: string, content: string, citations: Array, metadata: object, parent_revision_id: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function createRevision(docId, { content, authorKind, authorLabel, citations, metadata, parentRevisionId }) {
  const body = {
    content,
    author_kind: authorKind,
    author_label: authorLabel,
  };
  if (citations !== undefined) {
    body.citations = citations;
  }
  if (metadata !== undefined) {
    body.metadata = metadata;
  }
  if (parentRevisionId) {
    body.parent_revision_id = parentRevisionId;
  }

  const res = await fetchWithRenewal(`/api/etext/documents/${encodeURIComponent(docId)}/revisions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Create revision failed (${res.status})`);
  }

  return res.json();
}

/**
 * Lists revisions for a document.
 *
 * @param {string} docId - The document ID.
 * @returns {Promise<{revisions: Array}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function listRevisions(docId) {
  const res = await fetchWithRenewal(`/api/etext/documents/${encodeURIComponent(docId)}/revisions`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `List revisions failed (${res.status})`);
  }

  return res.json();
}

/**
 * Gets a specific revision (snapshot).
 *
 * Opening a historical revision does not mutate the document head
 * (VAL-ETEXT-007).
 *
 * @param {string} revisionId - The revision ID.
 * @returns {Promise<{revision_id: string, doc_id: string, owner_id: string, author_kind: string, author_label: string, content: string, citations: Array, metadata: object, parent_revision_id: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function getRevision(revisionId) {
  const res = await fetchWithRenewal(`/api/etext/revisions/${encodeURIComponent(revisionId)}`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Get revision failed (${res.status})`);
  }

  return res.json();
}

// ---------------------------------------------------------------------------
// History, Diff, Blame
// ---------------------------------------------------------------------------

/**
 * Gets the revision history for a document with explicit attribution
 * metadata (VAL-ETEXT-006).
 *
 * @param {string} docId - The document ID.
 * @returns {Promise<{doc_id: string, entries: Array<{revision_id: string, doc_id: string, author_kind: string, author_label: string, created_at: string, summary: string, parent_revision_id: string}>}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function getHistory(docId) {
  const res = await fetchWithRenewal(`/api/etext/documents/${encodeURIComponent(docId)}/history`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Get history failed (${res.status})`);
  }

  return res.json();
}

/**
 * Computes the diff between two revisions (VAL-ETEXT-008).
 *
 * @param {string} fromRevisionId - The from revision ID.
 * @param {string} toRevisionId - The to revision ID.
 * @returns {Promise<{from_revision_id: string, to_revision_id: string, sections: Array, added_lines: number, removed_lines: number}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function getDiff(fromRevisionId, toRevisionId) {
  const params = new URLSearchParams({
    from: fromRevisionId,
    to: toRevisionId,
  });
  const res = await fetchWithRenewal(`/api/etext/diff?${params.toString()}`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Get diff failed (${res.status})`);
  }

  return res.json();
}

/**
 * Gets the blame for a revision (VAL-ETEXT-009).
 *
 * @param {string} revisionId - The revision ID to blame.
 * @returns {Promise<{revision_id: string, doc_id: string, sections: Array<{revision_id: string, author_kind: string, author_label: string, start_line: number, end_line: number, content: string, timestamp: string}>}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function getBlame(revisionId) {
  const res = await fetchWithRenewal(`/api/etext/revisions/${encodeURIComponent(revisionId)}/blame`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Get blame failed (${res.status})`);
  }

  return res.json();
}

// ---------------------------------------------------------------------------
// Agent revision
// ---------------------------------------------------------------------------

/**
 * Submits an agent revision request for a document (VAL-ETEXT-003).
 *
 * Submitting a natural-language revision request creates a runtime task
 * that, when completed, creates a canonical appagent-authored revision.
 * Returns a stable task handle so the client can track progress through
 * the event stream (VAL-ETEXT-004).
 *
 * If a pending agent mutation already exists for this document (e.g.,
 * from a previous request that is still in-flight), the existing task
 * ID is returned instead of creating a new mutation, preventing
 * duplicate canonical revisions (VAL-CROSS-122).
 *
 * Uses fetchWithRenewal so renewal/retry does not duplicate the
 * mutation (VAL-CROSS-122).
 *
 * @param {string} docId - The document ID.
 * @param {string} prompt - The natural-language revision request.
 * @returns {Promise<{task_id: string, doc_id: string, state: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function submitAgentRevision(docId, prompt) {
  const res = await fetchWithRenewal(`/api/etext/documents/${encodeURIComponent(docId)}/agent-revision`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Agent revision failed (${res.status})`);
  }

  return res.json();
}

// ---------------------------------------------------------------------------
// Research worker (choir-in-choir)
// ---------------------------------------------------------------------------

/**
 * Spawns a research worker task for a document (VAL-CHOIR-007).
 *
 * The Research button creates a top-level task that runs in the background
 * (non-blocking). The task uses the document content as context for research.
 * Results are reported back to the etext editor when the worker completes.
 *
 * This uses the standard task submission endpoint (/api/agent/task) rather
 * than the spawn endpoint (/api/agent/spawn) because the research worker is
 * a standalone task — it does not need a parent task reference. The task
 * metadata includes the document context for the runtime to use.
 *
 * Uses fetchWithRenewal so renewal/retry does not duplicate the task
 * (VAL-CHOIR-012).
 *
 * @param {string} docId - The document ID being researched.
 * @param {string} content - Current document content (used as research context).
 * @param {string} topic - Research topic or query (defaults to document-based research).
 * @returns {Promise<{task_id: string, state: string, owner_id: string, created_at: string}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function spawnResearchWorker(docId, content, topic) {
  const prompt = topic && topic.trim()
    ? `Research the following topic and provide detailed findings:\n\nTopic: ${topic.trim()}\n\nDocument context:\n${content}`
    : `Research the following document content and provide detailed findings, additional context, and relevant information:\n\n${content}`;

  const res = await fetchWithRenewal('/api/agent/task', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      prompt,
      metadata: {
        type: 'etext_research',
        doc_id: docId,
      },
    }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Research task submission failed (${res.status})`);
  }

  return res.json();
}

/**
 * Fetches the status of a research worker task.
 *
 * @param {string} taskId - The task ID returned by spawnResearchWorker.
 * @returns {Promise<{task_id: string, state: string, result: string, error: string, metadata: object}>}
 * @throws {AuthRequiredError} If auth renewal fails.
 */
export async function fetchResearchStatus(taskId) {
  const res = await fetchWithRenewal(`/api/agent/status?task_id=${encodeURIComponent(taskId)}`, {
    method: 'GET',
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Research status fetch failed (${res.status})`);
  }

  return res.json();
}
