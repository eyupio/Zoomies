<!--
  Setting up an authenticator: the QR code, the key for anybody who cannot
  scan it, and the first code, which is what proves the app and this
  controller agree before anything changes at sign-in.

  The QR code is drawn by the controller, as SVG, and shown as an image: the
  key never passes through a third-party script, and an image cannot run
  anything. It is dark on white in both themes on purpose -- a scanner wants
  contrast, and some apps will not read a code drawn light on dark.

  Used on the account page and, when the instance requires two-step, in the
  middle of signing in. The caller decides where the key comes from and what
  confirming does; this decides how it looks.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { KeyRound, ScanLine } from '@lucide/svelte';
  import { ApiError } from '$lib/api/client';
  import type { TwoStepSetup } from '$lib/api/types';
  import { sentence } from '$lib/errors';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    /** Mint a new key. Called once, when the component appears. */
    load: () => Promise<TwoStepSetup>;
    /** Turn it on with the first code. Throw an ApiError to refuse it. */
    confirm: (code: string) => Promise<void>;
    /** "Turn on" on the account page; "Turn on and sign in" at sign-in. */
    confirmLabel?: string;
    /** lg on the sign-in page, where the form is the whole screen. */
    size?: 'md' | 'lg';
  }

  let { load, confirm, confirmLabel = 'Turn on', size = 'md' }: Props = $props();

  let setup = $state<TwoStepSetup | null>(null);
  let loadError = $state<unknown>(null);
  let code = $state('');
  let error = $state('');
  let busy = $state(false);
  let showKey = $state(false);
  let codeInput = $state<HTMLInputElement | null>(null);

  const qrSource = $derived(
    setup ? `data:image/svg+xml;charset=utf-8,${encodeURIComponent(setup.qr_svg)}` : '',
  );
  /** The key in groups of four, which is how people copy a long string by eye. */
  const groupedKey = $derived(setup?.secret.match(/.{1,4}/g)?.join(' ') ?? '');
  const digits = $derived(code.replace(/\D/g, ''));

  async function fetchKey(): Promise<void> {
    loadError = null;
    setup = null;
    try {
      setup = await load();
    } catch (cause) {
      loadError = cause;
    }
  }

  $effect(() => {
    untrack(() => void fetchKey());
  });

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (digits.length !== 6) {
      error = 'Enter the six digits your app shows for Zoomies.';
      codeInput?.focus();
      return;
    }
    busy = true;
    error = '';
    try {
      await confirm(digits);
    } catch (cause) {
      error =
        cause instanceof ApiError
          ? sentence(cause.fieldErrors().code ?? cause.message)
          : 'That code could not be checked. Try again.';
      code = '';
      codeInput?.focus();
    } finally {
      busy = false;
    }
  }
</script>

{#if loadError}
  <ErrorState error={loadError} onretry={fetchKey} />
{:else}
  <div class="enrol">
    <ol class="steps">
      <li>
        <span class="step-title">
          <ScanLine size={15} aria-hidden="true" />
          Scan this with your authenticator app
        </span>
        <div class="scan">
          {#if setup}
            <img
              class="qr"
              src={qrSource}
              alt="QR code for adding this account to an authenticator app. The same key is available as text below."
            />
          {:else}
            <Skeleton width="11rem" height="11rem" />
          {/if}
          <div class="key-side">
            <p class="hint">
              1Password, Bitwarden, Google or Microsoft Authenticator and most password managers can
              read it.
            </p>
            {#if showKey && setup}
              <p class="key-label" id="two-step-key-label">Your key</p>
              <p class="key mono" aria-labelledby="two-step-key-label">{groupedKey}</p>
              <CopyButton value={setup.secret} label="Copy the key" size="md" showLabel />
            {:else}
              <Button
                variant="ghost"
                icon={KeyRound}
                disabled={!setup}
                onclick={() => (showKey = true)}
              >
                Enter the key instead
              </Button>
            {/if}
          </div>
        </div>
      </li>
      <li>
        <span class="step-title">Type the code it shows</span>
        <form class="confirm" onsubmit={submit} novalidate>
          <Field label="Six-digit code" error={error || undefined}>
            {#snippet children({ id, describedBy, invalid })}
              <Input
                bind:value={code}
                bind:element={codeInput}
                {id}
                {describedBy}
                {invalid}
                name="code"
                inputmode="numeric"
                autocomplete="one-time-code"
                spellcheck={false}
                mono
                {size}
              />
            {/snippet}
          </Field>
          <Button type="submit" variant="primary" {size} loading={busy} disabled={!setup}>
            {confirmLabel}
          </Button>
        </form>
      </li>
    </ol>
  </div>
{/if}

<style>
  .enrol {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .steps {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .step-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin-bottom: var(--z-space-3);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .scan {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: var(--z-space-4);
  }
  /* A one-off measure: big enough for a phone camera held at arm's length,
     small enough to leave the key beside it on a laptop. */
  .qr {
    flex: none;
    width: 11rem;
    height: 11rem;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
  }
  .key-side {
    display: flex;
    flex: 1 1 12rem;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .hint,
  .key-label {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .key {
    margin: 0;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    overflow-wrap: anywhere;
  }
  .confirm {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
</style>
