<!--
  One machine, for the moment it is taking longer than it should.

  The page is the order of the questions: where is it, how long has each phase
  taken, what went wrong last time and when will it be tried again, and which
  handle to paste into the provider's own task log to see the other half of the
  story. That last one is the whole reason the operation id is on the page:
  every step Zoomies takes has a mark at the provider, and an operator
  who cannot join the two has to guess.

  Nothing here polls. The machine arrives once and its own frames are merged
  into it, so the timeline's last row keeps counting without anything being
  pressed.
-->
<script lang="ts">
  import { CircleSlash, Trash2 } from '@lucide/svelte';
  import { deleteMachine, drainMachine, getMachine } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Machine } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { router } from '$lib/router';
  import { machineStatus } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import MachineTimeline from '$lib/providers/MachineTimeline.svelte';
  import { machineSignals } from '$lib/providers/machines';

  const id = $derived(router.params.id ?? '');
  const canOperate = $derived(session.can('operator'));
  const canAdmin = $derived(session.can('admin'));

  let machine = $state<Machine | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);
  let showing = '';
  let deleteOpen = $state(false);

  $effect(() => {
    const machineId = id;
    if (machineId === '') return;
    if (showing !== machineId) {
      showing = machineId;
      machine = null;
    }
    void reload;
    const controller = new AbortController();
    loading = true;
    void getMachine(machineId, controller.signal)
      .then((row) => {
        machine = row;
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  $effect(() => {
    const machineId = id;
    if (machineId === '') return;
    const stop = events.subscribe('machine.updated', (row) => {
      if (row.id === machineId) machine = row;
    });
    return () => stop();
  });

  $effect(() => {
    if (machine?.name) router.setTitle(machine.name);
  });

  const status = $derived(machineStatus(machine?.state));
  const signals = $derived(machine ? machineSignals(machine) : null);
  // A machine still on its way, or draining, or being deleted: the last row of
  // the timeline is still being lived through and should keep counting.
  const running = $derived(
    machine?.state !== undefined &&
      machine.state !== 'deleted' &&
      machine.state !== 'failed' &&
      machine.state !== 'quarantined',
  );

  async function drain(): Promise<void> {
    if (!machine?.id) return;
    try {
      machine = await drainMachine(machine.id);
      toasts.info(
        `${machine.name} is draining`,
        'Its runners finish their jobs. The machine is deleted when the last one does.',
      );
    } catch (cause) {
      toasts.fromError(cause, 'That machine was not drained');
    }
  }

  async function remove(): Promise<boolean> {
    if (!machine?.id) return true;
    try {
      machine = await deleteMachine(machine.id);
      toasts.success(
        `Deleting ${machine.name}`,
        'It is not gone until the provider says the resource cannot be found.',
      );
      return true;
    } catch (cause) {
      // A 409 names the runners still on it. Keeping the dialog open lets the
      // operator read that and decide, which is the point of the refusal.
      toasts.fromError(cause, 'That machine was not deleted');
      return false;
    }
  }
</script>

{#if loading && !machine}
  <PageHeader title="Machine" breadcrumb={[{ label: 'Providers', href: '/providers' }]} />
  <Skeleton lines={6} />
{:else if error || !machine}
  <PageHeader title="Machine" breadcrumb={[{ label: 'Providers', href: '/providers' }]} />
  <ErrorState {error} title="That machine could not be read" onretry={() => (reload += 1)} />
{:else}
  <!-- One narrowed name for the whole page: `{#if}` narrowing does not reach
       inside the snippet bodies the header and the panels are made of. -->
  {@const m = machine}
  <PageHeader
    title={m.name || m.id || 'Machine'}
    breadcrumb={[
      { label: 'Providers', href: '/providers' },
      {
        label: m.provider_name || 'Provider',
        href: m.provider_id ? `/providers/${m.provider_id}` : undefined,
      },
      { label: m.name ?? '' },
    ]}
    onrefresh={async () => {
      reload += 1;
    }}
  >
    {#snippet meta()}
      <p class="summary">
        <Badge {status} size="sm" title={status.hint} />
        {#if m.message}<span>{m.message}</span>{/if}
      </p>
    {/snippet}
    {#if m.state === 'ready'}
      <Button icon={CircleSlash} disabled={!canOperate} onclick={() => void drain()}>Drain</Button>
    {/if}
    {#if canAdmin && m.state !== 'deleted'}
      <Button variant="danger" icon={Trash2} onclick={() => (deleteOpen = true)}>Delete</Button>
    {/if}
  </PageHeader>

  <div class="content">
    <Panel title="Where it is" flush>
      <dl class="facts">
        <div>
          <dt>Provider</dt>
          <dd>{m.provider_name || m.provider_id}</dd>
        </div>
        <div>
          <dt>Resource</dt>
          <dd class="mono">
            {#if m.resource_id}
              {m.resource_zone} / {m.resource_id}
            {:else}
              Not created yet
            {/if}
          </dd>
        </div>
        {#if m.address}
          <div>
            <dt>Address</dt>
            <dd class="mono">{m.address}</dd>
          </div>
        {/if}
        <div>
          <dt>Host</dt>
          <dd>
            {#if m.host_id}
              <a href="/hosts">{m.host_name || m.host_id}</a>
            {:else}
              Not enrolled yet
            {/if}
          </dd>
        </div>
        <div>
          <dt>Asked for by</dt>
          <dd>
            {#if m.pool_id}
              <a href="/pools/{m.pool_id}">{m.pool_name || m.pool_id}</a>'s unmet demand. It serves
              every pool its labels match, not only that one.
            {:else}
              Nothing recorded.
            {/if}
          </dd>
        </div>
        <div>
          <dt>Ownership</dt>
          <dd>
            {#if m.ownership_error}
              <span class="bad">{m.ownership_error}</span>
            {:else if m.ownership_verified_at}
              Confirmed ours <RelativeTime value={m.ownership_verified_at} plain />.
            {:else}
              Nothing has confirmed this resource is ours yet.
            {/if}
          </dd>
        </div>
        <div>
          <dt>Safe to delete</dt>
          <dd>
            {#if signals?.safeToDelete}
              Yes, as far as the row can say. The delete asks the provider again before it acts.
            {:else}
              No, {signals?.safeToDeleteWhy || 'the row does not say why.'}
            {/if}
          </dd>
        </div>
      </dl>
    </Panel>

    <Panel
      title="How it got here"
      description="One row per phase it actually reached. A phase it skipped is absent rather than shown as nothing."
    >
      <MachineTimeline entries={m.timeline ?? []} {running} />
    </Panel>

    {#if m.operation || m.operation_id}
      <Panel
        title="What it is doing"
        description="The operation in flight, and the handle that finds the other half of it in the provider's own task log."
      >
        <dl class="facts">
          {#if m.operation}
            <div>
              <dt>Operation</dt>
              <dd>
                {m.operation}
                {#if m.operation_since}
                  · running <Duration from={m.operation_since} live />
                {/if}
              </dd>
            </div>
          {/if}
          {#if m.operation_id}
            <div>
              <dt>Operation id</dt>
              <dd class="handle">
                <code class="mono">{m.operation_id}</code>
                <CopyButton value={m.operation_id} label="Copy the operation id" />
              </dd>
            </div>
          {/if}
          {#if m.operation_handle}
            <div>
              <dt>Provider's handle</dt>
              <dd class="handle">
                <code class="mono">{m.operation_handle}</code>
                <CopyButton value={m.operation_handle} label="Copy the provider's own handle" />
              </dd>
            </div>
          {/if}
          {#if m.operation_holder}
            <div>
              <dt>Taken by</dt>
              <dd class="mono">{m.operation_holder}</dd>
            </div>
          {/if}
          {#if m.outcome_unknown}
            <div>
              <dt>Outcome</dt>
              <dd class="bad">
                A call went out and was never answered, so what happened is not known. Nothing will
                be tried again until an observation settles it: a timeout is not evidence that
                nothing happened.
              </dd>
            </div>
          {/if}
        </dl>
      </Panel>
    {/if}

    {#if m.provider_error || m.bootstrap_error}
      <Panel
        title="The last thing that went wrong"
        description="Two records, because two independent systems can fail and a success at one must not erase the other's complaint."
      >
        <dl class="facts">
          {#if m.provider_error}
            <div>
              <dt>At the provider</dt>
              <dd><RemedyText text={m.provider_error} /></dd>
            </div>
          {/if}
          {#if m.bootstrap_error}
            <div>
              <dt>Inside the guest</dt>
              <dd><RemedyText text={m.bootstrap_error} /></dd>
            </div>
          {/if}
          <div>
            <dt>Attempts</dt>
            <dd>
              {pluralise(m.attempts ?? 0, 'attempt')} so far.
              {#if m.next_attempt_at}
                The next is <RelativeTime value={m.next_attempt_at} plain />.
              {:else}
                Nothing is scheduled; it is waiting on an observation or on a person.
              {/if}
            </dd>
          </div>
        </dl>
      </Panel>
    {/if}
  </div>

  <ConfirmDialog
    bind:open={deleteOpen}
    title="Delete this machine"
    name={m.name}
    requireName
    confirmLabel="Delete it"
    description="The resource behind this machine is destroyed at the provider."
    consequences={[
      'Any runner still on it stops, and the job it was running fails.',
      'It is not counted as gone until an inspection cannot find it.',
      'Nothing rebuilds it. A pool with work will ask for a new machine instead.',
    ]}
    onconfirm={remove}
  />
{/if}

<style>
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .summary {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .facts {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: var(--z-space-4) var(--z-space-5);
  }
  .facts div {
    display: grid;
    grid-template-columns: 12rem minmax(0, 1fr);
    gap: var(--z-space-3);
  }
  dt {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .handle {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  code {
    font-size: var(--z-text-xs);
  }
  .bad {
    color: var(--z-danger);
  }
  @media (max-width: 768px) {
    .facts div {
      grid-template-columns: 1fr;
      gap: var(--z-nudge-1);
    }
  }
</style>
