<!--
  First run.

  This form exists only while no account does: the API refuses it forever after,
  which is the whole security of the route. Say that plainly, because an
  operator who sees an unauthenticated "create an administrator" page deserves
  to know why it is safe.
-->
<script lang="ts">
  import { ApiError } from '$lib/api/client';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
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
  let form = $state<HTMLFormElement | null>(null);

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
    <Logo variant="full" size={34} label="Zoomies" />
  </div>
  <h1>Create the first administrator</h1>
  <p class="lede">
    Nobody has an account on this controller yet. This form creates the first one, with the admin
    role, and stops being available the moment it exists.
  </p>

  {#if failure}
    <p class="failure" role="alert">
      {failure.status === 409
        ? 'An account already exists, so this form has closed. Reload the page and sign in.'
        : failure.message}
    </p>
  {/if}

  <form bind:this={form} onsubmit={submit} novalidate>
    <Field label="Username" error={usernameError ?? fieldErrors.username} required>
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

    <Field label="Password" hint={strength} error={passwordError ?? fieldErrors.password} required>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={password}
          {id}
          {describedBy}
          {invalid}
          type="password"
          name="new-password"
          autocomplete="new-password"
          onblur={() => (touched = { ...touched, password: true })}
        />
      {/snippet}
    </Field>

    <Field label="Confirm the password" error={confirmError} required>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={confirm}
          {id}
          {describedBy}
          {invalid}
          type="password"
          autocomplete="new-password"
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
        <Input bind:value={email} {id} {describedBy} {invalid} type="email" autocomplete="email" />
      {/snippet}
    </Field>

    <Button type="submit" variant="primary" full loading={submitting}
      >Create the administrator</Button
    >
  </form>
</div>

<style>
  .brand {
    display: flex;
    justify-content: center;
    margin-bottom: var(--z-space-6);
  }
  .card {
    width: 100%;
    max-width: 420px;
    padding: var(--z-space-8);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-sm);
  }
  h1 {
    margin: 0;
    font-size: var(--z-text-xl);
    line-height: var(--z-leading-xl);
    font-weight: var(--z-weight-bold);
    color: var(--z-text);
  }
  .lede {
    margin: var(--z-space-3) 0 var(--z-space-6);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .failure {
    margin: 0 0 var(--z-space-4);
    padding: var(--z-space-3);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-danger-subtle);
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
</style>
