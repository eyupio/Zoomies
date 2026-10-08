<!--
  Your own account.

  Small, and first in the section, because of one specific moment: an
  administrator resets somebody's password, the shell tells them to change it
  in Settings, and this is what they have to find when they get here. When that
  is the case the card says so and the button is the primary one on the page.

  Refreshing asks who we are again, which is how a role granted a minute ago
  starts to apply without signing out and in.
-->
<script lang="ts">
  import { KeyRound, LogOut } from '@lucide/svelte';
  import { ApiError, logoutOthers } from '$lib/api/client';
  import { MIN_PASSWORD_LENGTH } from '$lib/passwords';
  import { roleLabel } from '$lib/roles';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import TwoStepPanel from '$lib/twostep/TwoStepPanel.svelte';

  let open = $state(false);
  let current = $state('');
  let next = $state('');
  let repeat = $state('');
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});

  const lengthError = $derived(
    next.length === 0 || next.length >= MIN_PASSWORD_LENGTH
      ? ''
      : `At least ${MIN_PASSWORD_LENGTH} characters. This one has ${next.length}.`,
  );
  const matchError = $derived(repeat.length > 0 && repeat !== next ? 'These do not match.' : '');
  const ready = $derived(next.length >= MIN_PASSWORD_LENGTH && repeat === next);

  /** The first letter of the name, for the avatar. */
  const initial = $derived(session.displayName.trim().charAt(0).toUpperCase() || '?');

  function start(): void {
    current = '';
    next = '';
    repeat = '';
    errors = {};
    open = true;
  }

  let othersOpen = $state(false);

  async function signOutOthers(): Promise<boolean> {
    try {
      const result = await logoutOthers();
      const n = result.mcp_connections_ended ?? 0;
      toasts.success(
        'Signed out everywhere else',
        n === 0
          ? 'Every other session signed in as you has ended.'
          : `Every other session has ended, and ${n === 1 ? '1 MCP connection' : `${n} MCP connections`} with them.`,
      );
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'Your other sessions were not signed out');
      return false;
    }
  }

  async function change(): Promise<void> {
    if (!ready) return;
    saving = true;
    errors = {};
    try {
      await session.changePassword(current || undefined, next);
      toasts.success(
        'Password changed',
        'Every other session signed in as you has been signed out, and your MCP connections ended.',
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

<PageHeader
  title="Account"
  subtitle="Who you are signed in as, your password, and two-step verification."
  onrefresh={() => session.refresh()}
/>

<div class="card" class:urgent={session.mustChangePassword}>
  <span class="avatar" aria-hidden="true">{initial}</span>
  <div class="who">
    <p class="name">Signed in as {session.displayName}</p>
    <p class="detail">
      {#if session.authDisabled}
        Authentication is switched off in the configuration, so every request is treated as an
        administrator.
      {:else if session.mustChangePassword}
        Your password was set by somebody else. Choose your own now.
      {:else}
        {roleLabel(session.role)}. Changing your password ends every other session signed in as you,
        and every MCP connection.
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
    {#if session.identity?.kind === 'user'}
      <Button icon={LogOut} onclick={() => (othersOpen = true)}>Sign out other sessions</Button>
    {/if}
  {/if}
</div>

<ConfirmDialog
  bind:open={othersOpen}
  title="Sign out other sessions"
  description="Every other browser signed in as you is signed out. This one stays signed in."
  consequences={[
    'Every MCP connection you have made, Claude, or any other client, ends too, and has to be approved again.',
  ]}
  confirmLabel="Sign out the others"
  onconfirm={signOutOthers}
/>

{#if !session.authDisabled && session.identity?.kind === 'user'}
  <TwoStepPanel />
{/if}

<Dialog
  bind:open
  title="Change your password"
  description="Every other session signed in as you is ended, and every MCP connection."
  size="sm"
>
  <form
    id="change-password-form"
    class="form"
    novalidate
    onsubmit={(event) => {
      event.preventDefault();
      void change();
    }}
  >
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
      hint="At least {MIN_PASSWORD_LENGTH} characters."
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
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="change-password-form"
      loading={saving}
      disabled={!ready}
    >
      Change password
    </Button>
  {/snippet}
</Dialog>

<style>
  .card {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .card.urgent {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
  }
  .avatar {
    display: inline-grid;
    place-items: center;
    flex: none;
    width: var(--z-space-10);
    height: var(--z-space-10);
    border-radius: var(--z-radius-full);
    background: var(--z-accent-subtle);
    color: var(--z-accent);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .who {
    flex: 1 1 16rem;
    min-width: 0;
  }
  .name {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .detail {
    margin: var(--z-nudge-2) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
</style>
