<!--
  App — root Svelte component.

  Checks auth state on mount via GET /auth/session. Renders the guest
  auth entry UI when signed out and a placeholder shell when signed in.

  Does NOT eagerly call protected routes (/api/shell/bootstrap, /api/ws)
  while the user is signed out.
-->
<script>
  import AuthEntry from './lib/AuthEntry.svelte';

  /** @type {'checking' | 'signed_out' | 'signed_in'} */
  let authState = 'checking';

  /** Current authenticated user, if any. */
  let currentUser = null;

  async function checkSession() {
    try {
      const res = await fetch('/auth/session', {
        method: 'GET',
        credentials: 'include',
      });
      if (!res.ok) {
        authState = 'signed_out';
        return;
      }
      const data = await res.json();
      if (data.authenticated && data.user) {
        authState = 'signed_in';
        currentUser = data.user;
      } else {
        authState = 'signed_out';
      }
    } catch (_err) {
      // Network error or unreachable — stay signed out.
      authState = 'signed_out';
    }
  }

  function handleAuthBegin(event) {
    // The authbegin event from AuthEntry signals that the user wants
    // to start a passkey ceremony. The actual WebAuthn flow wiring
    // is handled by the passkey-integration feature. For now, this
    // handler is a no-op placeholder so the UI is testable.
    const { username, type } = event.detail;
    console.log(`authbegin: ${type} for ${username}`);
  }

  import { onMount } from 'svelte';
  onMount(() => {
    checkSession();
  });
</script>

{#if authState === 'checking'}
  <div class="loading">
    <p>Loading…</p>
  </div>
{:else if authState === 'signed_out'}
  <AuthEntry on:authbegin={handleAuthBegin} />
{:else if authState === 'signed_in'}
  <!--
    Placeholder shell — will be replaced by the real desktop shell
    in a later feature. Kept minimal here so the signed-out guest
    UI is clearly distinct from the authenticated experience.
  -->
  <main class="shell-placeholder">
    <h1>go-choir</h1>
    <p class="subtitle">Distributed Multiagent Operating System</p>
    <p class="session-info">Signed in as {currentUser?.username || 'unknown'}</p>
  </main>
{/if}

<style>
  :global(*) {
    margin: 0;
    padding: 0;
    box-sizing: border-box;
  }

  :global(body) {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen,
      Ubuntu, Cantarell, 'Fira Sans', 'Droid Sans', 'Helvetica Neue', sans-serif;
    background: #0f0f0f;
    color: #e0e0e0;
  }

  .loading {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    color: #888;
  }

  .shell-placeholder {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    text-align: center;
  }

  .shell-placeholder h1 {
    font-size: 3rem;
    font-weight: 700;
    letter-spacing: 0.05em;
    color: #ffffff;
  }

  .shell-placeholder .subtitle {
    margin-top: 0.5rem;
    font-size: 1.25rem;
    font-weight: 300;
    color: #a0a0a0;
  }

  .shell-placeholder .session-info {
    margin-top: 1rem;
    font-size: 0.9rem;
    color: #6b7280;
    padding: 0.4rem 0.8rem;
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 6px;
  }
</style>
