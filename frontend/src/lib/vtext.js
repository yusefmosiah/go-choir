/**
 * VText API client for the go-choir desktop shell.
 *
 * Communicates with the versioned document APIs through the same-origin proxy:
 *   POST   /api/vtext/documents                   — create a new document
 *   GET    /api/vtext/documents                   — list documents
 *   GET    /api/vtext/documents/{id}              — get a document
 *   PUT    /api/vtext/documents/{id}              — update a document (title)
 *   DELETE /api/vtext/documents/{id}              — delete a document
 *   POST   /api/vtext/documents/{id}/revisions    — create a revision
 *   GET    /api/vtext/documents/{id}/revisions    — list revisions
 *   GET    /api/vtext/revisions/{id}              — get a revision (snapshot)
 *   GET    /api/vtext/documents/{id}/history      — revision history
 *   GET    /api/vtext/diff?from=X&to=Y            — diff two revisions
 *   GET    /api/vtext/revisions/{id}/blame        — blame revision
 *   POST   /api/vtext/documents/{id}/agent-revision — submit agent revision
 *
 * Conductor and worker tasks still use /api/agent/* because those APIs are
 * runtime-wide rather than document-specific.
 */

import { fetchWithRenewal } from './auth.js';

function vtextPath(path) {
  return `/api/vtext${path}`;
}

async function decodeError(res, fallback) {
  const err = await res.json().catch(() => ({}));
  throw new Error(err.error || fallback);
}

export async function createDocument(title) {
  const res = await fetchWithRenewal(vtextPath('/documents'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  });

  if (!res.ok) {
    await decodeError(res, `Create document failed (${res.status})`);
  }

  return res.json();
}

export async function listDocuments() {
  const res = await fetchWithRenewal(vtextPath('/documents'), {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `List documents failed (${res.status})`);
  }

  return res.json();
}

export async function getDocument(docId) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}`), {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Get document failed (${res.status})`);
  }

  return res.json();
}

export async function updateDocument(docId, title) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}`), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  });

  if (!res.ok) {
    await decodeError(res, `Update document failed (${res.status})`);
  }

  return res.json();
}

export async function deleteDocument(docId) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}`), {
    method: 'DELETE',
  });

  if (!res.ok) {
    await decodeError(res, `Delete document failed (${res.status})`);
  }

  return res.json();
}

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

  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}/revisions`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    await decodeError(res, `Create revision failed (${res.status})`);
  }

  return res.json();
}

export async function listRevisions(docId) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}/revisions`), {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `List revisions failed (${res.status})`);
  }

  return res.json();
}

export async function getRevision(revisionId) {
  const res = await fetchWithRenewal(vtextPath(`/revisions/${encodeURIComponent(revisionId)}`), {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Get revision failed (${res.status})`);
  }

  return res.json();
}

export async function getHistory(docId) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}/history`), {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Get history failed (${res.status})`);
  }

  return res.json();
}

export async function getDiff(fromRevisionId, toRevisionId) {
  const params = new URLSearchParams({
    from: fromRevisionId,
    to: toRevisionId,
  });
  const res = await fetchWithRenewal(vtextPath(`/diff?${params.toString()}`), {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Get diff failed (${res.status})`);
  }

  return res.json();
}

export async function getBlame(revisionId) {
  const res = await fetchWithRenewal(vtextPath(`/revisions/${encodeURIComponent(revisionId)}/blame`), {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Get blame failed (${res.status})`);
  }

  return res.json();
}

export async function createAgentRevision(docId, prompt) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}/agent-revision`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  });

  if (!res.ok) {
    await decodeError(res, `Agent revision failed (${res.status})`);
  }

  return res.json();
}

export async function submitAgentRevision(docId, prompt) {
  return createAgentRevision(docId, prompt);
}

export async function getAgentRevisionStatus(taskId) {
  const res = await fetchWithRenewal(`/api/agent/status?task_id=${encodeURIComponent(taskId)}`, {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Agent revision status fetch failed (${res.status})`);
  }

  return res.json();
}
