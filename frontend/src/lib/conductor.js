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

  const res = await fetchWithRenewal('/api/agent/task', {
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
