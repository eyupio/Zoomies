<!--
  Two-step verification, on the account page.

  Four states, each with one obvious next thing: not available (single
  sign-on, where the identity provider owns the second factor), off (turn it
  on), on (how many recovery codes are left, new ones, turn it off), and --
  where the instance requires it -- a note that turning it off only lasts until
  the next sign-in.

  Turning it off and issuing new recovery codes both ask for the password and
  a current code, because a borrowed session cookie is neither. The recovery
  codes appear in the same dialog that produced them and nowhere else.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ShieldCheck, ShieldOff, RefreshCw } from '@lucide/svelte';
  import {
    ApiError,
    confirmTwoStep,
    disableTwoStep,
    getTwoStep,
    regenerateRecoveryCodes,
    setupTwoStep,
  } from '$lib/api/client';
  import type { TwoStepStatus } from '$lib/api/types';
  import { sentence } from '$lib/errors';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import RecoveryCodes from './RecoveryCodes.svelte';
  import TwoStepEnrol from './TwoStepEnrol.svelte';

  let status = $state<TwoStepStatus | null>(null);
  let failed = $state(false);

  async function refresh(): Promise<void> {
    try {
      status = await getTwoStep();
      failed = false;
    } catch {
      failed = true;
    }
  }

  $effect(() => {
    untrack(() => void refresh());
  });

  /* -- turning it on ------------------------------------------------------- */

  let enrolOpen = $state(false);
  let issued = $state<string[] | null>(null);

  function startEnrol(): void {
    issued = null;
    enrolOpen = true;
  }

  async function confirmEnrol(code: string): Promise<void> {
    const out = await confirmTwoStep({ code });
    issued = out.recovery_codes;
    toasts.success(
      'Two-step verification is on',
      'Every other session signed in as you has been signed out.',
    );
    await refresh();
  }

  /* -- turning it off, and new codes --------------------------------------- */

  type Reauth = 'disable' | 'codes';
  let reauth = $state<Reauth | null>(null);
  let reauthOpen = $state(false);
  let password = $state('');
  let code = $state('');
  let busy = $state(false);
  let errors = $state<Record<string, string>>({});

  function startReauth(kind: Reauth): void {
    reauth = kind;
    password = '';
    code = '';
    errors = {};
    issued = null;
    reauthOpen = true;
  }

  async function submitReauth(event?: SubmitEvent): Promise<void> {
    event?.preventDefault();
    if (!password || !code.trim()) {
      errors = {
        ...(password ? {} : { password: 'Enter your password.' }),
        ...(code.trim() ? {} : { code: 'Enter a code from your app, or a recovery code.' }),
      };
      return;
    }
    busy = true;
    errors = {};
    try {
      if (reauth === 'disable') {
        await disableTwoStep({ password, code });
        toasts.success(
          'Two-step verification is off',
          status?.required
            ? 'This instance requires it, so you will set it up again at your next sign-in.'
            : 'Your password alone signs you in now.',
        );
        reauthOpen = false;
      } else {
        const out = await regenerateRecoveryCodes({ password, code });
        issued = out.recovery_codes;
      }
      await refresh();
    } catch (cause) {
      if (cause instanceof ApiError) {
        errors = Object.fromEntries(
          Object.entries(cause.fieldErrors()).map(([k, v]) => [k, sentence(v)]),
        );
      }
      if (!Object.keys(errors).length) toasts.fromError(cause, 'That was not changed');
    } finally {
      busy = false;
      password = '';
    }
  }
</script>

<section class="card" aria-labelledby="two-step-heading">
  <span class="icon" aria-hidden="true"><ShieldCheck size={18} /></span>
  <div class="body">
    <h2 id="two-step-heading" class="title">
      Two-step verification
      {#if status?.enabled}<Badge tone="accent" label="On" size="sm" />{/if}
      {#if status?.required && !status.enabled && status.available}
        <Badge tone="accent" label="Required" size="sm" />
      {/if}
    </h2>
    {#if failed}
      <p class="detail">Could not read your two-step verification. Refresh to try again.</p>
    {:else if !status}
      <Skeleton lines={2} />
    {:else if !status.available}
      <p class="detail">
        You sign in through single sign-on, so your identity provider handles your second factor.
        Ask whoever runs it if you want to change that.
      </p>
    {:else if status.enabled}
      <p class="detail">
        A code from your authenticator app is asked for after your password.
        {#if status.enabled_at}Turned on <RelativeTime value={status.enabled_at} />.{/if}
        <strong class:low={status.recovery_codes_left <= 2}>
          {status.recovery_codes_left} of 10 recovery codes left.
        </strong>
      </p>
    {:else}
      <p class="detail">
        Add a code from an authenticator app to your sign-in, so a password that leaks is not enough
        on its own.
        {#if status.required}
          This instance requires it: you will be asked to set it up at your next sign-in.
        {/if}
      </p>
    {/if}
  </div>
  {#if status?.available && !session.authDisabled}
    <div class="actions">
      {#if status.enabled}
        <Button variant="secondary" icon={RefreshCw} onclick={() => startReauth('codes')}>
          New recovery codes
        </Button>
        <Button variant="ghost" icon={ShieldOff} onclick={() => startReauth('disable')}>
          Turn off
        </Button>
      {:else}
        <Button variant="primary" icon={ShieldCheck} onclick={startEnrol}>
          Turn on two-step verification
        </Button>
      {/if}
    </div>
  {/if}
</section>

<Dialog
  bind:open={enrolOpen}
  title={issued ? 'Two-step verification is on' : 'Turn on two-step verification'}
  description={issued
    ? 'Keep these codes before you close this. They will not be shown again.'
    : 'Nothing changes at sign-in until you type a code from the app.'}
  dismissible={!issued}
>
  {#if issued}
    <RecoveryCodes codes={issued} account={session.identity?.name ?? ''} />
  {:else if enrolOpen}
    <TwoStepEnrol load={setupTwoStep} confirm={confirmEnrol} />
  {/if}
  {#snippet footer()}
    {#if issued}
      <Button variant="primary" onclick={() => (enrolOpen = false)}>I have saved them</Button>
    {:else}
      <Button variant="ghost" onclick={() => (enrolOpen = false)}>Cancel</Button>
    {/if}
  {/snippet}
</Dialog>

<Dialog
  bind:open={reauthOpen}
  title={issued
    ? 'Your new recovery codes'
    : reauth === 'disable'
      ? 'Turn off two-step verification'
      : 'Issue new recovery codes'}
  description={issued
    ? 'Every earlier recovery code has stopped working.'
    : reauth === 'disable'
      ? 'Your password alone will sign you in.'
      : 'Every recovery code you have now stops working.'}
  size="sm"
  dismissible={!issued}
>
  {#if issued}
    <RecoveryCodes codes={issued} account={session.identity?.name ?? ''} />
  {:else}
    <form id="two-step-reauth" class="form" onsubmit={submitReauth} novalidate>
      <Field label="Password" error={errors.password}>
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={password}
            {id}
            {describedBy}
            {invalid}
            name="password"
            type="password"
            autocomplete="current-password"
          />
        {/snippet}
      </Field>
      <Field label="Code from your app" hint="Or one of your recovery codes." error={errors.code}>
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={code}
            {id}
            {describedBy}
            {invalid}
            name="code"
            autocomplete="one-time-code"
            spellcheck={false}
            mono
          />
        {/snippet}
      </Field>
    </form>
  {/if}
  {#snippet footer()}
    {#if issued}
      <Button variant="primary" onclick={() => (reauthOpen = false)}>I have saved them</Button>
    {:else}
      <Button variant="ghost" onclick={() => (reauthOpen = false)}>Cancel</Button>
      <Button
        type="submit"
        form="two-step-reauth"
        variant={reauth === 'disable' ? 'danger' : 'primary'}
        loading={busy}
      >
        {reauth === 'disable' ? 'Turn off' : 'Issue new codes'}
      </Button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .card {
    display: flex;
    align-items: flex-start;
    flex-wrap: wrap;
    gap: var(--z-space-4);
    margin-top: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .icon {
    display: inline-grid;
    place-items: center;
    flex: none;
    width: var(--z-space-10);
    height: var(--z-space-10);
    border-radius: var(--z-radius-full);
    background: var(--z-accent-subtle);
    color: var(--z-accent);
  }
  .body {
    flex: 1 1 16rem;
    min-width: 0;
  }
  .title {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .detail {
    margin: var(--z-nudge-2) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .detail strong {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .detail strong.low {
    color: var(--z-danger);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
</style>
