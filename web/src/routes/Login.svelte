<!--
  Sign in.

  What this renders is decided by `/meta`, which is safe to call before anyone
  is authenticated: a password form, an SSO button, both, or -- when
  authentication has been switched off in the configuration -- a plain statement
  of that fact rather than a form that would do nothing.

  This is the first screen anyone sees, and often the only one they see while
  something is wrong, so it does the small things properly: the cursor starts in
  the first empty field, caps lock is called out before it costs an attempt, the
  password can be revealed, and a failure says which kind of failure it was --
  wrong credentials, too many attempts, or a controller that cannot be reached
  at all. Those are three different problems and only one of them is the
  operator's fault.
-->
<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { Eye, EyeOff, TriangleAlert } from '@lucide/svelte';
  import { ApiError, oidcStartUrl } from '$lib/api/client';
  import { authFailureText, sentence } from '$lib/errors';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import { DEVELOPER_NAME, DEVELOPER_URL, SITE_HOST, SITE_URL } from '$lib/links';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';

  let username = $state('');
  let password = $state('');
  let touched = $state({ username: false, password: false });
  let submitting = $state(false);
  let failure = $state<ApiError | null>(null);
  /**
   * Why a single sign-on attempt was sent back here. The controller cannot
   * render a page of its own for a failed SSO callback, so it redirects to
   * /login with the reason in the query string; it is read once and then
   * dropped from the address, so a reload does not repeat a stale complaint.
   */
  let ssoFailure = $state(
    untrack(() => new URLSearchParams(location.search).get('error')?.trim() ?? ''),
  );
  let revealed = $state(false);
  let capsLock = $state(false);
  let usernameInput = $state<HTMLInputElement | null>(null);
  let passwordInput = $state<HTMLInputElement | null>(null);

  /**
   * Where to go once signed in.
   *
   * Anything other than the overview is a deep link somebody followed while
   * signed out -- GitHub returning an operator to the App setup address with a
   * single-use code in it, most importantly -- and sending them to the overview
   * throws it away.
   */
  const destination = untrack(() =>
    location.pathname === '/login' ? '/' : location.pathname + location.search,
  );

  const meta = $derived(session.meta);

  $effect(() => {
    if (ssoFailure) router.setQuery({ error: null });
  });

  const usernameError = $derived(
    touched.username && username.trim() === '' ? 'Enter your username.' : undefined,
  );
  const passwordError = $derived(
    touched.password && password === '' ? 'Enter your password.' : undefined,
  );

  /**
   * What actually went wrong, in the operator's terms. A controller that never
   * answered and a password that was refused look identical in a generic
   * "sign-in failed", and they need completely different next steps.
   *
   * A 401 and a 403 are shown in the server's own words. It distinguishes a
   * wrong password from a disabled account and from an account that signs in
   * through SSO, and a 403 on this route is the origin check refusing the
   * request -- a proxy or external_url problem, which "wrong password" would
   * send the operator off to fix in entirely the wrong place.
   */
  const failureText = $derived.by(() => {
    if (!failure) return ssoFailure ? sentence(ssoFailure) : '';
    if (failure.status === 429) {
      return 'Too many sign-in attempts from this address. Wait a minute, then try again.';
    }
    return authFailureText(failure);
  });

  /*
    The cursor starts where there is something to type: a browser that has
    filled the username in should not make the operator tab past it.

    `placed` is a plain variable rather than state on purpose. The effect must
    fire once, when the fields first exist, and never again -- tracking it, or
    reading `username` reactively, would move the cursor out of the field
    somebody is typing in.
  */
  let placed = false;
  $effect(() => {
    if (placed || !usernameInput || meta?.auth_disabled) return;
    placed = true;
    untrack(() => (username.trim() === '' ? usernameInput : passwordInput))?.focus();
  });

  /**
   * Caps lock costs an attempt and, at the rate limit, a minute. The state is
   * only knowable from a key event, so it is read from every one the two
   * fields see and cleared when the password field is left.
   */
  function readCapsLock(event: KeyboardEvent): void {
    if (typeof event.getModifierState !== 'function') return;
    capsLock = event.getModifierState('CapsLock');
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    touched = { username: true, password: true };
    if (username.trim() === '' || password === '') {
      // Derived from the model, not from the DOM: Svelte batches state into a
      // microtask, so a query for `aria-invalid="true"` here matches nothing on
      // the first submit of an empty form -- which is precisely the keyboard
      // user pressing Enter that this line exists for.
      (username.trim() === '' ? usernameInput : passwordInput)?.focus();
      return;
    }
    submitting = true;
    failure = null;
    ssoFailure = '';
    try {
      await session.login(username.trim(), password);
      router.navigate(destination);
    } catch (cause) {
      failure =
        cause instanceof ApiError
          ? cause
          : new ApiError({ status: 0, code: 'internal', message: 'Sign-in failed. Try again.' });
      // The password is cleared, so that is where the cursor belongs: retyping
      // it is the next thing to do whichever failure this was. The field is
      // untouched again with it -- "Enter your password." under a field this
      // page emptied itself is an accusation, and it would sit directly below
      // the banner that already said what went wrong.
      password = '';
      touched = { ...touched, password: false };
      revealed = false;
      // After the flush: the failure box appears above the form and the field
      // is emptied in the same update, and focus set before that lands on an
      // element the render is about to move.
      void tick().then(() => passwordInput?.focus());
    } finally {
      submitting = false;
    }
  }
</script>

<div class="card">
  <div class="brand">
    <Logo variant="lockup" size={96} label="Zoomies" />
  </div>

  {#if meta?.auth_disabled}
    <h1>No sign-in required</h1>
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
    <h1>Sign in</h1>
    <p class="lede">Manage the runner fleet on this controller.</p>

    {#if failureText}
      <p class="failure" role="alert">
        <TriangleAlert size={15} aria-hidden="true" />
        <span>{failureText}</span>
      </p>
    {/if}

    <form onsubmit={submit} novalidate>
      <Field label="Username" error={usernameError}>
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={username}
            bind:element={usernameInput}
            {id}
            {describedBy}
            {invalid}
            name="username"
            autocomplete="username"
            autocapitalize="none"
            spellcheck={false}
            disabled={submitting}
            onkeydown={readCapsLock}
            onblur={() => (touched = { ...touched, username: true })}
          />
        {/snippet}
      </Field>

      <!-- The caps-lock warning goes in `notice`, not `hint`: hint is the
           branch Field drops the moment there is an error, which is exactly
           when caps lock is most likely to be the reason for one. -->
      <Field
        label="Password"
        hint="The one you chose when this controller was set up."
        error={passwordError}
        notice={capsLock ? 'Caps lock is on.' : undefined}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={password}
            bind:element={passwordInput}
            {id}
            {describedBy}
            {invalid}
            type={revealed ? 'text' : 'password'}
            name="password"
            autocomplete="current-password"
            disabled={submitting}
            onkeydown={readCapsLock}
            onblur={() => {
              touched = { ...touched, password: true };
              capsLock = false;
            }}
          >
            {#snippet trailing()}
              <IconButton
                icon={revealed ? EyeOff : Eye}
                label={revealed ? 'Hide password' : 'Show password'}
                size="sm"
                pressed={revealed}
                disabled={submitting}
                onclick={() => {
                  revealed = !revealed;
                  passwordInput?.focus();
                }}
              />
            {/snippet}
          </Input>
        {/snippet}
      </Field>

      <Button type="submit" variant="primary" full loading={submitting}>Sign in</Button>
    </form>

    {#if meta?.oidc_enabled}
      <div class="divider"><span>or</span></div>
      <Button href={oidcStartUrl()} full>{meta.oidc_label ?? 'Sign in with SSO'}</Button>
    {/if}
  {/if}
</div>

<p class="colophon">
  {#if meta?.version}<span class="version">Zoomies {meta.version}</span>{/if}
  <a href={SITE_URL} target="_blank" rel="noopener noreferrer">{SITE_HOST}</a>
  <span class="credit">
    Developed by
    <a href={DEVELOPER_URL} target="_blank" rel="noopener noreferrer">{DEVELOPER_NAME}</a>
  </span>
</p>

<style>
  /*
    The card floats on the page rather than sitting in a layout, so it carries
    real elevation. A hairline highlight along its top edge keeps it from
    reading as a flat rectangle in dark mode, where the border alone nearly
    disappears.
  */
  .card {
    position: relative;
    width: 100%;
    max-width: 25rem;
    padding: var(--z-space-8);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-lg);
  }
  .card::before {
    content: '';
    position: absolute;
    inset: 0 0 auto;
    height: 1px;
    margin: 0 var(--z-radius-lg);
    background: linear-gradient(90deg, transparent, var(--z-border-strong), transparent);
  }
  .brand {
    display: flex;
    justify-content: center;
    margin-bottom: var(--z-space-6);
    color: var(--z-text);
  }
  h1 {
    margin: 0;
    font-size: var(--z-text-xl);
    line-height: var(--z-leading-xl);
    font-weight: var(--z-weight-semibold);
    letter-spacing: -0.01em;
    color: var(--z-text);
    text-align: center;
  }
  .lede {
    margin: var(--z-space-2) 0 var(--z-space-6);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    text-align: center;
    text-wrap: balance;
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .failure {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-5);
    padding: var(--z-space-3);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-danger-subtle);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .failure :global(svg) {
    flex: none;
    margin-top: 1px;
    color: var(--z-danger);
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
    text-align: center;
  }
  /*
    Outside the card: a build number is about the installation, not about
    signing in, and it should not be the last thing inside the box.

    The project link sits beside it because this page is where Zoomies is met
    by people who did not install it -- a developer sent a URL by the operator
    who did. One quiet line is enough to tell them what they are looking at,
    and who makes it: the credit is the same one the site's footer carries.
  */
  .colophon {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: var(--z-space-1) var(--z-space-3);
    margin: 0;
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
  }
  .version {
    font-family: var(--z-font-mono);
  }
  .colophon a {
    color: inherit;
    text-decoration: none;
  }
  .colophon a:hover,
  .colophon a:focus-visible {
    color: var(--z-text-muted);
    text-decoration: underline;
  }
  .credit a {
    font-weight: var(--z-weight-medium);
  }
</style>
