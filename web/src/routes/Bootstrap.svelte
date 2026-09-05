<!--
  First run.

  This form exists only while no account does: the API refuses it forever after,
  which is the whole security of the route. Say that plainly, because an
  operator who sees an unauthenticated "create an administrator" page deserves
  to know why it is safe.
-->
<script lang="ts">
  import { Eye, EyeOff, TriangleAlert } from '@lucide/svelte';
  import { ApiError } from '$lib/api/client';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';

  /** The API's minimum. Long, rather than a zoo of character classes. */
  const MIN_LENGTH = 12;

  let username = $state('');
  let password = $state('');
  let confirm = $state('');
  let email = $state('');
  let touched = $state({ username: false, password: false, confirm: false });
  let submitting = $state(false);
  let failure = $state<ApiError | null>(null);
  let revealed = $state(false);
  let capsLock = $state(false);
  let form = $state<HTMLFormElement | null>(null);
  let usernameInput = $state<HTMLInputElement | null>(null);

  // Fires once, when the field first exists. Nothing here is prefilled, so the
  // cursor always belongs in the first one.
  let placed = false;
  $effect(() => {
    if (placed || !usernameInput) return;
    placed = true;
    usernameInput.focus();
  });

  /** Choosing a password with caps lock on is a password you cannot type again. */
  function readCapsLock(event: KeyboardEvent): void {
    if (typeof event.getModifierState !== 'function') return;
    capsLock = event.getModifierState('CapsLock');
  }

  const usernameError = $derived(
    touched.username && username.trim() === ''
      ? 'Choose a username for the administrator.'
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

  /** A hint that helps rather than scolds: length is what actually matters. */
  const strength = $derived.by(() => {
    if (password.length === 0) return `At least ${MIN_LENGTH} characters. A phrase works well.`;
    if (password.length < MIN_LENGTH) return `${password.length} of ${MIN_LENGTH} characters.`;
    if (password.length < 20) return 'Good. Longer is stronger than more punctuation.';
    return 'Strong.';
  });

  const fieldErrors = $derived(failure?.fieldErrors() ?? {});

  const valid = $derived(
    username.trim().length > 0 && password.length >= MIN_LENGTH && confirm === password,
  );

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    touched = { username: true, password: true, confirm: true };
    if (!valid) {
      form?.querySelector<HTMLInputElement>('input[aria-invalid="true"]')?.focus();
      return;
    }
    submitting = true;
    failure = null;
    try {
      await session.completeBootstrap({
        username: username.trim(),
        password,
        ...(email.trim() ? { email: email.trim() } : {}),
      });
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
    <Logo variant="lockup" size={96} label="" />
  </div>
  <h1>Create the first administrator</h1>
  <p class="lede">
    Nobody has an account on this controller yet. This form creates the first one, with the admin
    role, and stops being available the moment it exists.
  </p>

  {#if failure}
    <p class="failure" role="alert">
      <TriangleAlert size={15} aria-hidden="true" />
      <span>
        {failure.status === 409
          ? 'An account already exists, so this form has closed. Reload the page and sign in.'
          : failure.message}
      </span>
    </p>
  {/if}

  <form bind:this={form} onsubmit={submit} novalidate>
    <Field label="Username" error={usernameError ?? fieldErrors.username} required>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={username}
          bind:element={usernameInput}
          {id}
          {describedBy}
          {invalid}
          name="username"
          autocomplete="username"
          disabled={submitting}
          onkeydown={readCapsLock}
          onblur={() => (touched = { ...touched, username: true })}
        />
      {/snippet}
    </Field>

    <Field
      label="Password"
      hint={capsLock ? 'Caps lock is on.' : strength}
      error={passwordError ?? fieldErrors.password}
      required
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={password}
          {id}
          {describedBy}
          {invalid}
          type={revealed ? 'text' : 'password'}
          name="new-password"
          autocomplete="new-password"
          disabled={submitting}
          onkeydown={readCapsLock}
          onblur={() => (touched = { ...touched, password: true })}
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

    <Field label="Confirm the password" error={confirmError} required>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={confirm}
          {id}
          {describedBy}
          {invalid}
          type={revealed ? 'text' : 'password'}
          autocomplete="new-password"
          disabled={submitting}
          onkeydown={readCapsLock}
          onblur={() => (touched = { ...touched, confirm: true })}
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

    <Button type="submit" variant="primary" full loading={submitting}
      >Create the administrator</Button
    >
  </form>
</div>

<style>
  /* Deliberately identical to Login.svelte: these two are the same screen at
     two moments in a controller's life, and a card that changes width, weight
     or elevation between them reads as two different products. */
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
    text-wrap: balance;
  }
  .lede {
    margin: var(--z-space-2) 0 var(--z-space-6);
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
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
</style>
