<!--
  Sign in.

  What this renders is decided by `/meta`, which is safe to call before anyone
  is authenticated: a password form, an SSO button, both, or -- when
  authentication has been switched off in the configuration -- a plain statement
  of that fact rather than a form that would do nothing.
-->
<script lang="ts">
  import { ApiError, oidcStartUrl } from '$lib/api/client';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';

  let username = $state('');
  let password = $state('');
  let touched = $state({ username: false, password: false });
  let submitting = $state(false);
  let failure = $state<ApiError | null>(null);
  let form = $state<HTMLFormElement | null>(null);

  const meta = $derived(session.meta);

  const usernameError = $derived(
    touched.username && username.trim() === '' ? 'Enter your username.' : undefined,
  );
  const passwordError = $derived(
    touched.password && password === '' ? 'Enter your password.' : undefined,
  );

  const rateLimited = $derived(failure?.status === 429);

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    touched = { username: true, password: true };
    if (username.trim() === '' || password === '') {
      form?.querySelector<HTMLInputElement>('input[aria-invalid="true"]')?.focus();
      return;
    }
    submitting = true;
    failure = null;
    try {
      await session.login(username.trim(), password);
      router.navigate('/');
    } catch (cause) {
      failure =
        cause instanceof ApiError
          ? cause
          : new ApiError({ status: 0, code: 'internal', message: 'Sign-in failed. Try again.' });
      password = '';
    } finally {
      submitting = false;
    }
  }
</script>

<div class="card">
  <div class="brand">
    <Logo variant="lockup" size={104} label="" />
    <h1 class="sr-only">Zoomies</h1>
  </div>

  {#if meta?.auth_disabled}
    <p class="lede">
      Authentication is switched off in this controller's configuration, so there is nothing to sign
      in to. Anyone who can reach this address has full access.
    </p>
    <Button variant="primary" full href="/">Continue to the dashboard</Button>
    <p class="note">
      Turn authentication back on in the configuration file before this controller is reachable by
      anyone you do not trust.
    </p>
  {:else}
    <p class="lede">Sign in to manage the runner fleet.</p>

    {#if failure}
      <p class="failure" role="alert">
        {#if rateLimited}
          Too many sign-in attempts from this address. Wait a minute, then try again.
        {:else}
          {failure.message}
        {/if}
      </p>
    {/if}

    <form bind:this={form} onsubmit={submit} novalidate>
      <Field label="Username" error={usernameError}>
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={username}
            {id}
            {describedBy}
            {invalid}
            name="username"
            autocomplete="username"
            onblur={() => (touched = { ...touched, username: true })}
          />
        {/snippet}
      </Field>

      <Field label="Password" error={passwordError}>
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={password}
            {id}
            {describedBy}
            {invalid}
            type="password"
            name="password"
            autocomplete="current-password"
            onblur={() => (touched = { ...touched, password: true })}
          />
        {/snippet}
      </Field>

      <Button type="submit" variant="primary" full loading={submitting}>Sign in</Button>
    </form>

    {#if meta?.oidc_enabled}
      <div class="divider"><span>or</span></div>
      <Button href={oidcStartUrl()} full>{meta.oidc_label ?? 'Sign in with SSO'}</Button>
    {/if}
  {/if}

  {#if meta?.version}
    <p class="version">Zoomies {meta.version}</p>
  {/if}
</div>

<style>
  .card {
    width: 100%;
    max-width: 360px;
    padding: var(--z-space-8);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-sm);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    color: var(--z-accent);
  }
  .lede {
    margin: var(--z-space-4) 0 var(--z-space-6);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .failure {
    margin: 0 0 var(--z-space-4);
    padding: var(--z-space-3);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-danger-subtle);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .divider {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    margin: var(--z-space-5) 0;
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
  .divider::before,
  .divider::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--z-border);
  }
  .note {
    margin: var(--z-space-4) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .version {
    margin: var(--z-space-6) 0 0;
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
    text-align: center;
  }
</style>
