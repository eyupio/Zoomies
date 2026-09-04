<!--
  Your own account.

  Small, and above the tabs, because of one specific moment: an administrator
  resets somebody's password, the shell tells them to change it in Settings, and
  this is what they have to find when they get here. When that is the case the
  bar says so and the button is the primary one on the page.
-->
<script lang="ts">
  import { KeyRound } from '@lucide/svelte';
  import { ApiError } from '$lib/api/client';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';

  const MIN_PASSWORD = 12;

  let open = $state(false);
  let current = $state('');
  let next = $state('');
  let repeat = $state('');
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});

  const lengthError = $derived(
    next.length === 0 || next.length >= MIN_PASSWORD
      ? ''
      : `At least ${MIN_PASSWORD} characters. This one has ${next.length}.`,
  );
  const matchError = $derived(repeat.length > 0 && repeat !== next ? 'These do not match.' : '');
  const ready = $derived(next.length >= MIN_PASSWORD && repeat === next);

  function start(): void {
    current = '';
    next = '';
    repeat = '';
    errors = {};
    open = true;
  }

  async function change(): Promise<void> {
    if (!ready) return;
    saving = true;
    errors = {};
    try {
      await session.changePassword(current || undefined, next);
      toasts.success(
        'Password changed',
        'Every other session signed in as you has been signed out.',
      );
      open = false;
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'That password was not changed');
    } finally {
      saving = false;
    }
  }
</script>

<div class="bar" class:urgent={session.mustChangePassword}>
  <div class="who">
    <p class="name">Signed in as {session.displayName}</p>
    <p class="detail">
      {#if session.authDisabled}
        Authentication is switched off in the configuration, so every request is treated as an
        administrator.
      {:else if session.mustChangePassword}
        Your password was set by somebody else. Choose your own now.
      {:else}
        Role: {session.role ?? 'unknown'}.
      {/if}
    </p>
  </div>
  {#if !session.authDisabled}
    <Button
      variant={session.mustChangePassword ? 'primary' : 'secondary'}
      icon={KeyRound}
      onclick={start}
    >
      Change password
    </Button>
  {/if}
</div>

<Dialog
  bind:open
  title="Change your password"
  description="Every other session signed in as you is ended."
  size="sm"
>
  <div class="form">
    <Field
      label="Current password"
      hint="Leave empty if an administrator just reset it for you."
      error={errors.old_password}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={current}
          {id}
          {describedBy}
          {invalid}
          type="password"
          autocomplete="current-password"
        />
      {/snippet}
    </Field>

    <Field
      label="New password"
      hint="At least {MIN_PASSWORD} characters."
      error={errors.new_password ?? lengthError}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={next}
          {id}
          {describedBy}
          invalid={invalid || Boolean(lengthError)}
          type="password"
          autocomplete="new-password"
        />
      {/snippet}
    </Field>

    <Field label="New password again" error={matchError}>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={repeat}
          {id}
          {describedBy}
          invalid={invalid || Boolean(matchError)}
          type="password"
          autocomplete="new-password"
        />
      {/snippet}
    </Field>
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} disabled={!ready} onclick={change}>
      Change password
    </Button>
  {/snippet}
</Dialog>

<style>
  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-5);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .bar.urgent {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
  }
  .name {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .detail {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
</style>
