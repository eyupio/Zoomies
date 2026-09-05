<!--
  Enrolling a new host.

  A host joins by running one command, which carries a single-use join token.
  The dialog therefore has two states: the form that mints the token, and the
  command itself -- which is the only time the token is ever visible.
-->
<script lang="ts">
  import { ApiError, createJoinToken } from '$lib/api/client';
  import type { JoinToken } from '$lib/api/types';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import OneTimeSecret from '$lib/settings/OneTimeSecret.svelte';
  import LabelMapEditor from './LabelMapEditor.svelte';

  type Minted = JoinToken & { token?: string; command?: string };

  interface Props {
    open?: boolean;
    /** Called once a token has been minted, so the outstanding list can reload. */
    oncreated?: () => void;
    onclose?: () => void;
  }

  let { open = $bindable(false), oncreated, onclose }: Props = $props();

  const TTLS = [
    { value: '15m', label: '15 minutes' },
    { value: '1h', label: '1 hour' },
    { value: '24h', label: '24 hours' },
  ];

  let ttl = $state('15m');
  let capacity = $state('2');
  let rows = $state<{ key: string; value: string }[]>([]);
  let creating = $state(false);
  let errors = $state<Record<string, string>>({});
  let minted = $state<Minted | null>(null);

  $effect(() => {
    if (open) return;
    // Nothing is kept between openings: the token is gone the moment this closes.
    minted = null;
    errors = {};
    ttl = '15m';
    capacity = '2';
    rows = [];
  });

  const capacityNumber = $derived(Number(capacity));
  const capacityError = $derived(
    capacity.trim() === '' || !Number.isInteger(capacityNumber) || capacityNumber < 0
      ? 'Give a whole number of runners, or 0 to let the agent decide from the host’s CPU count.'
      : '',
  );

  async function mint(): Promise<void> {
    if (capacityError) return;
    creating = true;
    errors = {};
    const labels: Record<string, string> = {};
    for (const row of rows) {
      const key = row.key.trim();
      if (key) labels[key] = row.value.trim();
    }
    try {
      minted = await createJoinToken({ ttl, capacity: capacityNumber, labels });
      oncreated?.();
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'That join token was not created');
    } finally {
      creating = false;
    }
  }

  /**
   * The address the new host will reach this controller on.
   *
   * The server substitutes a placeholder when server.external_url is unset --
   * which is every deployment that did not come from `zoomies init` -- so the
   * command it returns cannot run. Rather than hand that over, the dialog asks,
   * defaulting to the address this browser is already using, which is right
   * far more often than not.
   */
  const PLACEHOLDER = 'https://<this-controller>';
  const needsControllerURL = $derived(Boolean(minted?.command?.includes(PLACEHOLDER)));
  let controllerURL = $state(location.origin);

  const joinCommand = $derived.by(() => {
    const command = minted?.command ?? minted?.token ?? '';
    if (!needsControllerURL) return command;
    return command.replace(PLACEHOLDER, controllerURL.trim().replace(/\/+$/, ''));
  });

  function close(): void {
    open = false;
    onclose?.();
  }
</script>

<Dialog
  bind:open
  title="Add a host"
  description={minted
    ? 'Run this on the new host. It enrols the agent and registers it with this controller.'
    : 'Zoomies mints a single-use join token, and the host enrols itself with it.'}
  size="lg"
  onclose={close}
>
  {#if minted}
    <div class="done">
      {#if needsControllerURL}
        <p class="unreachable">
          {session.meta?.external_url
            ? `This controller's external URL is ${session.meta.external_url}, which no other machine can reach.`
            : 'This controller does not know its own address.'}
          A host cannot join a controller on an address only that controller can reach, so the command
          below needs one the new machine will actually use.
        </p>
        <!--
          The command the server built carries a placeholder, because this
          controller does not know its own address. Presenting that under "Run
          this on the new host" hands the operator a line that cannot work --
          and the token is single use, so the failure costs them another one.
        -->
        <Field
          label="Controller URL"
          hint="Set server.external_url in the configuration and every operator gets the right command; for now, this fills in the line below."
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input bind:value={controllerURL} {id} {describedBy} {invalid} type="url" mono />
          {/snippet}
        </Field>
      {/if}
      <OneTimeSecret what="join token" value={joinCommand} copyLabel="Copy the install command" />
      <p class="expiry">
        It can be used once, and expires <RelativeTime value={minted.expires_at} plain />. If nobody
        gets to it in time, mint another.
      </p>
      {#if minted.command && minted.token}
        <details>
          <summary>Show the token on its own</summary>
          <p class="token mono">{minted.token}</p>
        </details>
      {/if}
    </div>
  {:else}
    <div class="form">
      <Field
        label="Valid for"
        hint="How long the token can be used before it expires. Shorter is safer."
        error={errors.ttl}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Select bind:value={ttl} options={TTLS} {id} {describedBy} {invalid} />
        {/snippet}
      </Field>

      <Field
        label="Capacity"
        hint="How many runners the new host may run at once. 0 lets the agent choose from the host’s CPU count."
        error={errors.capacity ?? capacityError}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={capacity}
            {id}
            {describedBy}
            invalid={invalid || Boolean(capacityError)}
            type="number"
            min={0}
            step={1}
          />
        {/snippet}
      </Field>

      <Field
        label="Labels"
        hint="Given to the host when it enrols. Pools use them to choose where runners go."
      >
        {#snippet children({ describedBy })}
          <LabelMapEditor bind:rows {describedBy} />
        {/snippet}
      </Field>
    </div>
  {/if}

  {#snippet footer()}
    {#if minted}
      <Button variant="primary" onclick={close}>Done</Button>
    {:else}
      <Button variant="ghost" onclick={close}>Cancel</Button>
      <Button variant="primary" loading={creating} disabled={Boolean(capacityError)} onclick={mint}>
        Mint a join token
      </Button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .form,
  .done {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  .expiry {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  summary {
    cursor: pointer;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .token {
    margin: var(--z-space-2) 0 0;
    padding: var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
    overflow-wrap: anywhere;
  }
  .unreachable {
    margin: 0;
    padding: var(--z-space-3);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    text-wrap: pretty;
  }
</style>
