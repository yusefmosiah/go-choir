<!--
  App — root Svelte component.

  Checks auth state on mount via GET /auth/session. Renders the guest
  auth entry UI when signed out and the placeholder desktop shell when
  signed in.

  Rehydration and renewal behaviour (VAL-CROSS-004 / VAL-CROSS-005):
    - On mount (including hard reload / new tab), checkSession() calls
      GET /auth/session which automatically does refresh rotation if the
      access JWT is expired but the refresh cookie is valid.
    - If the session is valid, the shell is rendered and bootstraps its
      protected routes (bootstrap + WS) using cookie-backed auth.
    - If both access and refresh state are invalid, the app falls back
      to the guest auth UI (VAL-CROSS-008).

  Does NOT eagerly call protected routes (/api/shell/bootstrap, /api/ws)
  while the user is signed out.

  Passkey ceremony errors (cancel/failure) keep the user in a retryable
  guest auth state and never reveal the authenticated shell.
-->
<script>
  import AuthEntry from './lib/AuthEntry.svelte';
  import Desktop from './lib/Desktop.svelte';
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
    const { email, type } = event.detail;
    passkeyError = '';
    ceremonyInProgress = true;

    try {
      if (type === 'register') {
        await registerPasskey(email);
      } else {
        await loginPasskey(email);
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

  /**
   * Handles the authexpired event from the Shell component.
   * When a protected request fails with 401 and renewal cannot restore
   * the session, the Shell dispatches this event. The app transitions
   * cleanly to the guest auth state (VAL-CROSS-008).
   */
  function handleAuthExpired() {
    authState = 'signed_out';
    currentUser = null;
    passkeyError = '';
  }

  import { onMount } from 'svelte';
  onMount(() => {
    checkSession();

    // Prevent bfcache from resurrecting an authenticated shell after
    // logout. When the page is restored from back/forward cache, the
    // old JavaScript state may still show the shell even though the
    // server-side session has been invalidated. Re-check the session
    // on pageshow to catch this case (VAL-CROSS-006).
    function handlePageShow(event) {
      if (event.persisted) {
        // Page was restored from bfcache — re-verify auth state.
        checkSession();
      }
    }
    window.addEventListener('pageshow', handlePageShow);

    // Also listen for focus events as a secondary guard: if the user
    // switches back to this tab after logging out in another tab or
    // context, we re-check the session.
    function handleFocus() {
      if (authState === 'signed_in') {
        // Only re-check if we think we're signed in — avoids
        // unnecessary session checks while already signed out.
        checkSession();
      }
    }
    window.addEventListener('focus', handleFocus);

    return () => {
      window.removeEventListener('pageshow', handlePageShow);
      window.removeEventListener('focus', handleFocus);
    };
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
  <Desktop {currentUser} on:logout={handleLogout} on:authexpired={handleAuthExpired} />
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
