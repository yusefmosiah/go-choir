<!--
  AuthEntry — guest auth entry experience for signed-out users.

  Exposes distinct register and login views, each with a clear primary
  action to begin the passkey flow. Does not call any protected
  (/api/shell/bootstrap, /api/ws) routes while signed out.

  Displays passkey ceremony errors (cancel/failure) from the parent
  component via the `passkeyError` prop, keeping the user in a
  retryable guest auth state.

  Data attributes for test targeting:
    data-auth-entry       — root container
    data-register-toggle  — control to switch to register view
    data-login-toggle     — control to switch to login view
    data-register-view    — register view container
    data-login-view       — login view container
    data-passkey-error    — passkey ceremony error message area
-->
<script>
  import { createEventDispatcher } from 'svelte';

  export let passkeyError = '';

  /** Whether a passkey ceremony is in progress (disables the form). */
  export let ceremonyInProgress = false;

  const dispatch = createEventDispatcher();

  /** @type {'register' | 'login'} */
  let view = 'register';

  /** Username input for the current view. */
  let username = '';

  /** Validation error message (empty username etc). */
  let error = '';

  /** Combined error to display: validation error takes precedence, then passkeyError. */
  $: displayError = error || passkeyError;

  function switchView(newView) {
    view = newView;
    username = '';
    error = '';
    // Clear passkeyError when switching views so the user gets a clean retry state.
    dispatch('clearpasskeyerror');
  }

  function handleRegister() {
    error = '';
    if (!username.trim()) {
      error = 'Please enter a username.';
      return;
    }
    dispatch('authbegin', { username: username.trim(), type: 'register' });
  }

  function handleLogin() {
    error = '';
    if (!username.trim()) {
      error = 'Please enter a username.';
      return;
    }
    dispatch('authbegin', { username: username.trim(), type: 'login' });
  }
</script>

<div class="auth-entry" data-auth-entry>
  <div class="auth-card">
    <h1>Welcome to go-choir</h1>
    <p class="tagline">Distributed Multiagent Operating System</p>

    <div class="view-tabs">
      <button
        class="tab"
        class:active={view === 'register'}
        data-register-toggle
        on:click={() => switchView('register')}
        disabled={ceremonyInProgress}
      >
        Register
      </button>
      <button
        class="tab"
        class:active={view === 'login'}
        data-login-toggle
        on:click={() => switchView('login')}
        disabled={ceremonyInProgress}
      >
        Sign In
      </button>
    </div>

    {#if view === 'register'}
      <div class="auth-view" data-register-view>
        <h2>Create Account</h2>
        <p class="view-desc">Register a new account with a passkey. No passwords needed.</p>

        <form on:submit|preventDefault={handleRegister}>
          <label for="register-username">Username</label>
          <input
            id="register-username"
            type="text"
            bind:value={username}
            placeholder="Choose a username"
            autocomplete="username"
            disabled={ceremonyInProgress}
            required
          />
          <button type="submit" class="primary-action" disabled={ceremonyInProgress} data-auth-submit>
            {#if ceremonyInProgress}
              Creating passkey…
            {:else}
              Register with Passkey
            {/if}
          </button>
        </form>
      </div>
    {:else}
      <div class="auth-view" data-login-view>
        <h2>Sign In</h2>
        <p class="view-desc">Log in with your registered passkey.</p>

        <form on:submit|preventDefault={handleLogin}>
          <label for="login-username">Username</label>
          <input
            id="login-username"
            type="text"
            bind:value={username}
            placeholder="Enter your username"
            autocomplete="username"
            disabled={ceremonyInProgress}
            required
          />
          <button type="submit" class="primary-action" disabled={ceremonyInProgress} data-auth-submit>
            {#if ceremonyInProgress}
              Signing in…
            {:else}
              Sign In with Passkey
            {/if}
          </button>
        </form>
      </div>
    {/if}

    {#if displayError}
      <p class="error" role="alert" data-passkey-error>{displayError}</p>
    {/if}
  </div>
</div>

<style>
  .auth-entry {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    width: 100%;
  }

  .auth-card {
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 12px;
    padding: 2.5rem 2rem;
    width: 100%;
    max-width: 400px;
    text-align: center;
  }

  h1 {
    font-size: 1.75rem;
    font-weight: 700;
    letter-spacing: 0.03em;
    color: #ffffff;
    margin-bottom: 0.25rem;
  }

  .tagline {
    font-size: 0.9rem;
    color: #888;
    margin-bottom: 1.5rem;
  }

  .view-tabs {
    display: flex;
    gap: 0;
    margin-bottom: 1.5rem;
    border-radius: 8px;
    overflow: hidden;
    border: 1px solid #2a2a2a;
  }

  .tab {
    flex: 1;
    padding: 0.6rem 1rem;
    font-size: 0.9rem;
    font-weight: 500;
    background: transparent;
    color: #999;
    border: none;
    cursor: pointer;
    transition: background 0.2s, color 0.2s;
  }

  .tab:hover {
    background: #222;
    color: #ccc;
  }

  .tab.active {
    background: #2a2a2a;
    color: #ffffff;
  }

  .auth-view {
    text-align: center;
  }

  .auth-view h2 {
    font-size: 1.25rem;
    font-weight: 600;
    color: #e0e0e0;
    margin-bottom: 0.5rem;
  }

  .view-desc {
    font-size: 0.85rem;
    color: #888;
    margin-bottom: 1.25rem;
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    text-align: left;
  }

  label {
    font-size: 0.8rem;
    font-weight: 500;
    color: #aaa;
  }

  input[type="text"] {
    padding: 0.7rem 0.85rem;
    font-size: 0.95rem;
    background: #111;
    border: 1px solid #333;
    border-radius: 8px;
    color: #e0e0e0;
    outline: none;
    transition: border-color 0.2s;
  }

  input[type="text"]:focus {
    border-color: #555;
  }

  input[type="text"]::placeholder {
    color: #555;
  }

  .primary-action {
    margin-top: 0.5rem;
    padding: 0.8rem 1rem;
    font-size: 1rem;
    font-weight: 600;
    background: #3b82f6;
    color: #ffffff;
    border: none;
    border-radius: 8px;
    cursor: pointer;
    transition: background 0.2s;
  }

  .primary-action:hover {
    background: #2563eb;
  }

  .primary-action:disabled {
    background: #1e3a5f;
    color: #667;
    cursor: not-allowed;
  }

  .error {
    margin-top: 1rem;
    color: #f87171;
    font-size: 0.85rem;
  }
</style>
