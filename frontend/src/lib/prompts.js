import { fetchWithRenewal } from './auth.js';

async function decodeError(res, fallback) {
  const err = await res.json().catch(() => ({}));
  throw new Error(err.error || fallback);
}

export async function listPrompts() {
  const res = await fetchWithRenewal('/api/prompts', { method: 'GET' });
  if (!res.ok) {
    await decodeError(res, `List prompts failed (${res.status})`);
  }
  return res.json();
}

export async function updatePrompt(role, content) {
  const res = await fetchWithRenewal(`/api/prompts/${encodeURIComponent(role)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  });
  if (!res.ok) {
    await decodeError(res, `Update prompt failed (${res.status})`);
  }
  return res.json();
}

export async function resetPrompt(role) {
  const res = await fetchWithRenewal(`/api/prompts/${encodeURIComponent(role)}`, {
    method: 'DELETE',
  });
  if (!res.ok) {
    await decodeError(res, `Reset prompt failed (${res.status})`);
  }
  return res.json();
}
