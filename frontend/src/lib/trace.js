import { fetchWithRenewal } from './auth.js';

async function decodeError(res, fallback) {
  const err = await res.json().catch(() => ({}));
  throw new Error(err.error || fallback);
}

export async function listAgentTasks(limit = 100) {
  const params = new URLSearchParams({ limit: String(limit) });
  const res = await fetchWithRenewal(`/api/agent/tasks?${params.toString()}`, {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Task list fetch failed (${res.status})`);
  }

  return res.json();
}

export async function listAgentEvents({ taskId = '', limit = 200 } = {}) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (taskId) {
    params.set('task_id', taskId);
  }

  const res = await fetchWithRenewal(`/api/agent/events?${params.toString()}`, {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Event list fetch failed (${res.status})`);
  }

  return res.json();
}

export async function getAgentTopology() {
  const res = await fetchWithRenewal('/api/agent/topology', {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Topology fetch failed (${res.status})`);
  }

  return res.json();
}

export function openEventStream({ afterSeq = 0, onEvent, onError } = {}) {
  const params = new URLSearchParams();
  if (afterSeq > 0) {
    params.set('after_seq', String(afterSeq));
  }
  const suffix = params.toString() ? `?${params.toString()}` : '';
  const source = new EventSource(`/api/events${suffix}`);

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
