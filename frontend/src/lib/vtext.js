/**
 * VText API client for the go-choir desktop shell.
 *
 * Communicates with the versioned document APIs through the same-origin proxy:
 *   POST   /api/vtext/documents                   — create a new document
 *   GET    /api/vtext/documents                   — list documents
 *   POST   /api/vtext/files/open                  — resolve/create aliased file doc
 *   POST   /api/vtext/documents/{id}/manifest     — ensure a filesystem manifestation
 *   GET    /api/vtext/documents/{id}              — get a document
 *   PUT    /api/vtext/documents/{id}              — update a document (title)
 *   DELETE /api/vtext/documents/{id}              — delete a document
 *   POST   /api/vtext/documents/{id}/revisions    — create a revision
 *   GET    /api/vtext/documents/{id}/revisions    — list revisions
 *   GET    /api/vtext/revisions/{id}              — get a revision (snapshot)
 *   GET    /api/vtext/documents/{id}/history      — revision history
 *   GET    /api/vtext/diff?from=X&to=Y            — diff two revisions
 *   GET    /api/vtext/revisions/{id}/blame        — blame revision
 *   GET    /api/vtext/documents/{id}/stream       — document-scoped stream
 *   POST   /api/vtext/documents/{id}/agent-revision — submit agent revision
 *
 * Conductor and worker loops still use /api/agent/* because those APIs are
 * runtime-wide rather than document-specific.
 */

import { fetchWithRenewal } from './auth.js';
import { withDesktopSelector } from './desktop-selector.js';

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

export async function openFileDocument({ sourcePath, title, initialContent }) {
  const res = await fetchWithRenewal(vtextPath('/files/open'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      source_path: sourcePath,
      title,
      initial_content: initialContent,
    }),
  });

  if (!res.ok) {
    await decodeError(res, `Open file document failed (${res.status})`);
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

export async function ensureDocumentManifest(docId) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}/manifest`), {
    method: 'POST',
  });

  if (!res.ok) {
    await decodeError(res, `Ensure document manifest failed (${res.status})`);
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

export async function createAgentRevision(docId, payload = {}) {
  const res = await fetchWithRenewal(vtextPath(`/documents/${encodeURIComponent(docId)}/agent-revision`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });

  if (!res.ok) {
    await decodeError(res, `Agent revision failed (${res.status})`);
  }

  return res.json();
}

export async function submitAgentRevision(docId, payload = {}) {
  return createAgentRevision(docId, payload);
}

export function openDocumentStream(docId, { onEvent, onError } = {}) {
  const source = new EventSource(withDesktopSelector(vtextPath(`/documents/${encodeURIComponent(docId)}/stream`)));

  source.onmessage = (event) => {
    if (!onEvent) return;
    try {
      onEvent(JSON.parse(event.data));
    } catch (err) {
      if (onError) onError(err);
    }
  };
  source.onerror = (err) => {
    if (onError) onError(err);
  };

  return source;
}
