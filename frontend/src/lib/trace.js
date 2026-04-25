import { fetchWithRenewal } from './auth.js';
import { withDesktopSelector } from './desktop-selector.js';

async function decodeError(res, fallback) {
  const err = await res.json().catch(() => ({}));
  throw new Error(err.error || fallback);
}

async function readJSON(path, fallback) {
  const res = await fetchWithRenewal(path, { method: 'GET' });
  if (!res.ok) {
    await decodeError(res, `${fallback} (${res.status})`);
  }
  return res.json();
}

export function listTrajectories(limit = 100) {
  return readJSON(`/api/trace/trajectories?limit=${encodeURIComponent(String(limit))}`, 'Trajectory list fetch failed');
}

export function getTrajectorySnapshot(trajectoryId) {
  return readJSON(`/api/trace/trajectories/${encodeURIComponent(trajectoryId)}`, 'Trajectory snapshot fetch failed');
}

export function getTrajectoryMomentDetail(trajectoryId, momentId) {
  return readJSON(
    `/api/trace/trajectories/${encodeURIComponent(trajectoryId)}/moments/${encodeURIComponent(momentId)}`,
    'Trajectory detail fetch failed',
  );
}

export function openTrajectoryEventStream(trajectoryId, { afterSeq = 0, onEvent, onError } = {}) {
  const params = new URLSearchParams();
  if (afterSeq > 0) {
    params.set('after_seq', String(afterSeq));
  }
  const suffix = params.toString() ? `?${params.toString()}` : '';
  const source = new EventSource(
    withDesktopSelector(`/api/trace/trajectories/${encodeURIComponent(trajectoryId)}/events${suffix}`),
  );

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
