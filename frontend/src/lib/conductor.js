import { fetchWithRenewal } from './auth.js';

function trimText(text) {
  return (text || '').trim();
}

export async function submitConductorPrompt(text, options = {}) {
  const prompt = trimText(text);
  if (!prompt) {
    throw new Error('Prompt is required');
  }

  const metadata = {
    agent_profile: 'conductor',
    agent_role: 'conductor',
    input_source: options.inputSource || 'prompt_bar',
    requested_app: options.requestedApp || 'vtext',
    seed_prompt: prompt,
  };

  if (options.initialDocumentTitle) {
    metadata.initial_document_title = options.initialDocumentTitle;
  }

  const res = await fetchWithRenewal('/api/agent/loop', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      prompt,
      metadata,
    }),
  });

  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Conductor submission failed (${res.status})`);
  }

  return res.json();
}

function parseDecision(raw) {
  if (!raw || typeof raw !== 'string') {
    throw new Error('Conductor returned an empty decision');
  }
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new Error('Conductor returned invalid decision JSON');
  }
  if (!parsed?.action) {
    throw new Error('Conductor decision is missing an action');
  }
  return parsed;
}

export async function waitForConductorDecision(loopId, options = {}) {
  if (!loopId) {
    throw new Error('Conductor loop ID is required');
  }

  const timeoutMs = options.timeoutMs ?? 60000;
  const pollMs = options.pollMs ?? 500;
  const deadline = Date.now() + timeoutMs;

  for (;;) {
    const res = await fetchWithRenewal(`/api/agent/status?loop_id=${encodeURIComponent(loopId)}`, {
      method: 'GET',
    });

    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || `Conductor status failed (${res.status})`);
    }

    const status = await res.json();
    if (typeof status.result === 'string' && status.result.trim()) {
      return parseDecision(status.result);
    }
    if (status.state === 'failed' || status.state === 'blocked' || status.state === 'cancelled') {
      throw new Error(status.error || `Conductor ${status.state}`);
    }
    if (Date.now() >= deadline) {
      throw new Error('Conductor decision timed out');
    }
    await new Promise((resolve) => setTimeout(resolve, pollMs));
  }
}
