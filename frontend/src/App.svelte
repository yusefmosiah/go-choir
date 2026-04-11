<!--
  App — root Svelte component.

  Checks auth state on mount via GET /auth/session. Renders the guest
  auth entry UI when signed out and the placeholder desktop shell when
  signed in.

  Does NOT eagerly call protected routes (/api/shell/bootstrap, /api/ws)
  while the user is signed out.

  Passkey ceremony errors (cancel/failure) keep the user in a retryable
  guest auth state and never reveal the authenticated shell.
-->
<script>
  import AuthEntry from './lib/AuthEntry.svelte';
  import Shell from './lib/Shell.svelte';
  import { registerPasskey, loginPasskey, passkeyErrorMessage } from './lib/auth.js';

  /** @type {'checking' | 'signed_out' | 'signed_in'} */
  let authState = 'checking';

  /** Current authenticated user, if any. */
  let currentUser = null;

  /** Passkey ceremony error message displayed in the AuthEntry. */
  let passkeyError = '';

  /** Whether a passkey ceremony is in progress. */
  let ceremonyInProgress = false;

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

  async function handleAuthBegin(event) {
    const { username, type } = event.detail;
    passkeyError = '';
    ceremonyInProgress = true;

    try {
      if (type === 'register') {
        await registerPasskey(username);
      } else {
        await loginPasskey(username);
      }

      // Ceremony succeeded — re-check session to transition to
      // the authenticated state.
      await checkSession();
    } catch (err) {
      // Ceremony failed or was cancelled — stay in signed-out
      // state and display a retryable error message.
      authState = 'signed_out';
      passkeyError = passkeyErrorMessage(err);
    } finally {
      ceremonyInProgress = false;
    }
  }

  function handleClearPasskeyError() {
    passkeyError = '';
  }

  async function handleLogout() {
    passkeyError = '';
    try {
      await fetch('/auth/logout', {
        method: 'POST',
        credentials: 'include',
      });
    } catch (_err) {
      // Logout request failed — still transition to signed-out
      // state locally so the user is not stuck in the shell.
    }
    authState = 'signed_out';
    currentUser = null;
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
  <AuthEntry
    {passkeyError}
    {ceremonyInProgress}
    on:authbegin={handleAuthBegin}
    on:clearpasskeyerror={handleClearPasskeyError}
  />
{:else if authState === 'signed_in'}
  <Shell {currentUser} on:logout={handleLogout} />
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
</style>
