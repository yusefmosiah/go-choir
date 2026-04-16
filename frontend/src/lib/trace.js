import { fetchWithRenewal } from './auth.js';

async function decodeError(res, fallback) {
  const err = await res.json().catch(() => ({}));
  throw new Error(err.error || fallback);
}

export async function listAgentRuns(limit = 100, { channelId = '' } = {}) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (channelId) {
    params.set('channel_id', channelId);
  }
  const res = await fetchWithRenewal(`/api/agent/runs?${params.toString()}`, {
    method: 'GET',
  });

  if (!res.ok) {
    await decodeError(res, `Run list fetch failed (${res.status})`);
  }

  return res.json();
}

export async function listAgentEvents({ runId = '', channelId = '', limit = 200 } = {}) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (runId) {
    params.set('run_id', runId);
  }
  if (channelId) {
    params.set('channel_id', channelId);
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
