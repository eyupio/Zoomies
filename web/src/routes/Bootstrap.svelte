<!--
  First run.

  This form exists only while no account does: the API refuses it forever after,
  which is the whole security of the route. Say that plainly, because an
  operator who sees an unauthenticated "create an administrator" page deserves
  to know why it is safe.

  It is also step one of three, and says so. An operator arriving here from
  `docker compose up` has no way to know whether this account finishes setup or
  begins it; naming the two steps that follow is what makes the checklist they
  land on next read as a continuation rather than an unexplained new panel.
-->
<script lang="ts">
  import { Eye, EyeOff, TriangleAlert } from '@lucide/svelte';
  import { tick } from 'svelte';
  import { ApiError } from '$lib/api/client';
  import { authFailureText } from '$lib/errors';
  import { MIN_PASSWORD_LENGTH, passwordStrength } from '$lib/passwords';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';

  const MIN_LENGTH = MIN_PASSWORD_LENGTH;

  let username = $state('');
  let password = $state('');
  let confirm = $state('');
  let email = $state('');
  let setupToken = $state('');
  let touched = $state({ username: false, password: false, confirm: false, setupToken: false });
  let submitting = $state(false);
  let failure = $state<ApiError | null>(null);
  let revealed = $state(false);
  let capsLock = $state(false);
  let setupTokenInput = $state<HTMLInputElement | null>(null);
  let usernameInput = $state<HTMLInputElement | null>(null);
  let passwordInput = $state<HTMLInputElement | null>(null);
  let confirmInput = $state<HTMLInputElement | null>(null);

  // Fires once, when the field first exists. Nothing here is prefilled, so the
  // cursor always belongs in the first one.
  let placed = false;
  $effect(() => {
    if (placed || !setupTokenInput) return;
    placed = true;
    setupTokenInput.focus();
  });

  /** Choosing a password with caps lock on is a password you cannot type again. */
  function readCapsLock(event: KeyboardEvent): void {
    if (typeof event.getModifierState !== 'function') return;
    capsLock = event.getModifierState('CapsLock');
  }

  const setupTokenError = $derived(
    touched.setupToken && setupToken.trim() === ''
      ? 'Paste the setup token from the controller log.'
      : undefined,
  );

  const usernameError = $derived(
    touched.username && username.trim() === ''
      ? 'Choose a username for the first account.'
      : undefined,
  );

  const passwordError = $derived(
    touched.password && password.length > 0 && password.length < MIN_LENGTH
      ? `A few more characters: ${MIN_LENGTH - password.length} to go.`
      : touched.password && password.length === 0
        ? 'Choose a password.'
        : undefined,
  );

  const confirmError = $derived(
    touched.confirm && confirm !== password ? 'The two passwords are not the same.' : undefined,
  );

  const strength = $derived(passwordStrength(password));

  const fieldErrors = $derived(failure?.fieldErrors() ?? {});

  const valid = $derived(
    setupToken.trim().length > 0 &&
      username.trim().length > 0 &&
      password.length >= MIN_LENGTH &&
      confirm === password,
  );

  /**
   * A 409 means somebody else won the race and this form has closed. The page
   * cannot fix that, but it can carry the operator to the one that can, rather
   * than telling them to reload it themselves.
   */
  const closed = $derived(failure?.status === 409);

  const failureText = $derived.by(() => {
    if (!failure) return '';
    if (closed) return 'An account already exists, so this form has closed. Sign in instead.';
    return authFailureText(failure);
  });

  /**
   * The first field that is not filled in yet.
   *
   * Deriving this from the model rather than querying for `aria-invalid="true"`
   * is the whole point: Svelte batches state into a microtask, so at the moment
   * a submit handler runs the DOM still carries the pre-submit attributes. The
   * query matched nothing on the first submit of an empty form -- exactly the
   * case a keyboard user hits -- and focus stayed on the button with no field
   * error spoken and nothing moved.
   */
  function firstInvalid(): HTMLInputElement | null {
    if (setupToken.trim() === '') return setupTokenInput;
    if (username.trim() === '') return usernameInput;
    if (password.length < MIN_LENGTH) return passwordInput;
    if (confirm !== password) return confirmInput;
    return null;
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    touched = { username: true, password: true, confirm: true, setupToken: true };
    if (!valid) {
      // After a tick, so the field carries its error before focus lands on it
      // and the screen reader reads the two together. The old code queried the
      // DOM for aria-invalid in the same synchronous block that set `touched`,
      // which matched nothing at all -- Svelte had not rendered it yet.
      void tick().then(() => firstInvalid()?.focus());
      return;
    }
    submitting = true;
    failure = null;
    try {
      const identity = await session.completeBootstrap({
        username: username.trim(),
        password,
        setup_token: setupToken.trim(),
        ...(email.trim() ? { email: email.trim() } : {}),
      });
      // The account exists; whether the session started with it is a separate
      // question, and the API can answer 201 without a cookie when it could
      // not. Confirming rather than assuming is what stops the operator being
      // dropped on an unexplained sign-in page after a form that worked.
      const signedIn = await session.confirmSignedIn();
      if (!signedIn) {
        failure = new ApiError({
          status: 0,
          code: 'internal',
          message: `The administrator ${identity?.name ?? username.trim()} was created, but the session could not be started. Reload this page and sign in with the password you just chose.`,
        });
        return;
      }
      // One sentence, because the page this lands on opens with the checklist
      // that says what is left; a second one repeated it under the toast.
      toasts.success(
        `Signed in as ${identity?.name ?? username.trim()}`,
        'Next: connect a GitHub App.',
      );
      router.navigate('/');
    } catch (cause) {
      failure =
        cause instanceof ApiError
          ? cause
          : new ApiError({
              status: 0,
              code: 'internal',
              message: 'The administrator could not be created. Check the controller log.',
            });
    } finally {
      submitting = false;
    }
  }
</script>

<div class="card">
  <div class="brand">
    <Logo variant="lockup" size={96} label="Zoomies" />
  </div>
  <!-- "First", not "1 of 4". How many steps there are depends on what this
       install is: a controller with an agent of its own has one fewer than one
       without, and this page runs before there is a session to ask with. The
       Overview's checklist counts the real list from the fleet's own state, so
       this says only which end of it we are at. -->
  <p class="step">First step</p>
  <h1>Create the first account</h1>
  <p class="lede">
    Nobody has an account on this instance yet. This form creates the first one, with the admin
    role, and stops being available the moment it exists. The setup token is how it knows you are
    the one who deployed Zoomies.
  </p>
  <p class="next">
    Then: connect a GitHub App, add a host if Zoomies has no agent of its own, create a pool, and
    point a workflow at it. The Overview keeps the list and ticks it off.
  </p>

  {#if failure}
    <div class="failure" role="alert">
      <TriangleAlert size={15} aria-hidden="true" />
      <div>
        <p>{failureText}</p>
        {#if closed}
          <!-- The page already knows the state is stale, so it flips itself
               rather than asking the operator to reload it by hand. -->
          <Button variant="secondary" size="sm" onclick={() => void session.boot()}>
            Go to sign in
          </Button>
        {/if}
      </div>
    </div>
  {/if}

  <form onsubmit={submit} novalidate>
    <!-- Every field carries a hint, so the message row is occupied before an
         error needs it and the form does not move as one appears. -->
    <Field
      label="Setup token"
      hint="From the controller's log: docker compose logs zoomies | grep 'setup token'"
      error={setupTokenError ?? fieldErrors.setup_token}
      required
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={setupToken}
          bind:element={setupTokenInput}
          {id}
          {describedBy}
          {invalid}
          name="setup-token"
          autocomplete="off"
          autocapitalize="none"
          spellcheck={false}
          mono
          disabled={submitting}
          onblur={() => (touched = { ...touched, setupToken: true })}
        />
      {/snippet}
    </Field>

    <Field
      label="Username"
      hint="You will sign in with this."
      error={usernameError ?? fieldErrors.username}
      required
    >
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

    <Field
      label="Password"
      hint={strength}
      notice={capsLock ? 'Caps lock is on.' : undefined}
      error={passwordError ?? fieldErrors.password}
      required
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
          autocomplete="new-password"
          minlength={MIN_LENGTH}
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
              onclick={() => (revealed = !revealed)}
            />
          {/snippet}
        </Input>
      {/snippet}
    </Field>

    <Field
      label="Confirm the password"
      hint="Type it again, so a slip cannot lock you out of your own controller."
      notice={capsLock ? 'Caps lock is on.' : undefined}
      error={confirmError}
      required
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={confirm}
          bind:element={confirmInput}
          {id}
          {describedBy}
          {invalid}
          type={revealed ? 'text' : 'password'}
          name="confirm-password"
          autocomplete="new-password"
          minlength={MIN_LENGTH}
          disabled={submitting}
          onkeydown={readCapsLock}
          onblur={() => {
            touched = { ...touched, confirm: true };
            capsLock = false;
          }}
        />
      {/snippet}
    </Field>

    <Field
      label="Email"
      hint="Optional. Used only to identify the account."
      error={fieldErrors.email}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={email}
          {id}
          {describedBy}
          {invalid}
          type="email"
          autocomplete="email"
          disabled={submitting}
        />
      {/snippet}
    </Field>

    <Button type="submit" variant="primary" full loading={submitting}>Create the account</Button>
  </form>

  <p class="version">Zoomies {session.meta?.version ?? ''}</p>
</div>

<style>
  /* A card, where sign-in is a page with a brand panel beside the form. The
     panel is for people who did not install the controller and have been sent
     its address; the only person who ever sees this screen has just installed
     it, knows what it is, and wants the form and nothing to read on the way to
     it. The card's width, weight and elevation are the ones the product's
     dialogs use, so it still reads as the same product. */
  .card {
    position: relative;
    width: 100%;
    max-width: 25rem;
    padding: var(--z-space-8);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-lg);
  }
  .card::before {
    content: '';
    position: absolute;
    inset: 0 0 auto;
    height: var(--z-border-width);
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
    letter-spacing: var(--z-tracking-tight);
    color: var(--z-text);
    text-align: center;
    text-wrap: balance;
  }
  .step {
    margin: 0 0 var(--z-space-1);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    letter-spacing: var(--z-tracking-wider);
    text-transform: uppercase;
    color: var(--z-text-subtle);
    text-align: center;
  }
  .next {
    margin: 0 0 var(--z-space-6);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
    text-align: center;
    text-wrap: pretty;
  }
  .version {
    margin: var(--z-space-5) 0 0;
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
    text-align: center;
  }
  .lede {
    margin: var(--z-space-2) 0 var(--z-space-2);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    text-align: center;
    text-wrap: pretty;
  }
  .failure {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-5);
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-danger-subtle);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .failure p {
    margin: 0;
    /* The http/https message is three sentences long and is the difference
       between finishing setup and giving up, so it gets room to be read. */
    text-wrap: pretty;
  }
  .failure div {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-3);
    min-width: 0;
  }
  .failure :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-1);
    color: var(--z-danger);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
</style>
