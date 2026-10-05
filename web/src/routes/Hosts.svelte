<!--
  Hosts: where runners can go.

  The list comes from the live fleet cache, so a host that stops sending
  heartbeats goes unhealthy here without anything being pressed. Everything that
  keeps a host from taking work -- cordoned, unhealthy, no capacity left, a
  backend that is not available -- is said in words on the card, because that is
  the question this page exists to answer.

  Two things on this page do not arrive over the stream, though, and that is
  what refreshing is for here: a host enrolled a moment ago, which the
  controller announces only once it has heard from the agent, and the join
  tokens, which move when somebody mints or spends one rather than when the
  fleet does.
-->
<script lang="ts">
  import HostCapacityMap from '$lib/insights/HostCapacityMap.svelte';
  import { hostSignals } from '$lib/insights/signals';
  import MetricGrid from '$lib/components/MetricGrid.svelte';
  import { Plus, Server } from '@lucide/svelte';
  import {
    clearHostThrottle,
    cordonHost,
    listJoinTokens,
    listMachines,
    listProviders,
    pauseProvider,
    resumeProvider,
  } from '$lib/api/client';
  import { SvelteMap } from 'svelte/reactivity';
  import { events } from '$lib/api/sse';
  import type { Host, JoinToken, Machine, Provider } from '$lib/api/types';
  import { NO_VALUE, pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import HostCard from '$lib/hosts/HostCard.svelte';
  import HostCapacityDialog from '$lib/hosts/HostCapacityDialog.svelte';
  import HostRunnerSizesDialog from '$lib/hosts/HostRunnerSizesDialog.svelte';
  import { slotsOf } from '$lib/hosts/slots';
  import HostDeleteDialog from '$lib/hosts/HostDeleteDialog.svelte';
  import HostRenameDialog from '$lib/hosts/HostRenameDialog.svelte';
  import HostLabelsDialog from '$lib/hosts/HostLabelsDialog.svelte';
  import JoinTokenList from '$lib/hosts/JoinTokenList.svelte';
  import MachineBand from '$lib/providers/MachineBand.svelte';

  const canOperate = $derived(session.can('operator'));
  const canAdmin = $derived(session.can('admin'));

  const hosts = $derived(
    [...fleet.hosts].sort((a, b) => (a.name ?? '').localeCompare(b.name ?? '')),
  );
  const healthy = $derived(hosts.filter((h) => h.healthy === true).length);
  // The slots hosts take, which is what the machines hold of a standard size
  // where one is set, not the sum of the operators' ceilings.
  const capacity = $derived(hosts.reduce((sum, h) => sum + slotsOf(h), 0));
  const inUse = $derived(hosts.reduce((sum, h) => sum + (h.active_runners ?? 0), 0));

  /* -- the machines behind the hosts -------------------------------------------
   * Fetched here and kept out of the fleet cache deliberately: a fourth
   * collection there would cost every signed-in tab a request on every
   * reconcile pass, and the app shell weight the budget has none of. The live
   * half is one subscription, which costs nothing when nobody is on this page.
   * ------------------------------------------------------------------------ */

  let machines = $state<Machine[]>([]);
  let providers = $state<Provider[]>([]);
  let machinesReload = $state(0);

  $effect(() => {
    void machinesReload;
    const controller = new AbortController();
    void Promise.all([
      listProviders(controller.signal),
      listMachines({ limit: 200 }, controller.signal),
    ])
      .then(([providerPage, machinePage]) => {
        providers = providerPage.items ?? [];
        machines = machinePage.items ?? [];
      })
      .catch((cause: unknown) => {
        // Not an error state. The hosts are the page; the band above them is
        // context, and a fleet with no providers configured answers 200 with
        // nothing in it anyway.
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
      });
    return () => controller.abort();
  });

  $effect(() => {
    const stop = [
      events.subscribe('machine.updated', (row) => {
        const index = machines.findIndex((m) => m.id === row.id);
        machines = index === -1 ? [...machines, row] : machines.with(index, row);
      }),
      events.subscribe('machine.deleted', (payload) => {
        machines = machines.filter((m) => m.id !== payload.id);
      }),
      events.subscribe('provider.updated', (row) => {
        providers = providers.map((p) => (p.id === row.id ? row : p));
      }),
    ];
    return () => {
      for (const off of stop) off();
    };
  });

  /** The machine a host is, for the hosts Zoomies rented. */
  const machineByHost = $derived.by(() => {
    const out = new SvelteMap<string, Machine>();
    for (const machine of machines) {
      if (machine.host_id) out.set(machine.host_id, machine);
    }
    return out;
  });

  async function pauseAll(paused: boolean): Promise<void> {
    for (const provider of providers) {
      if (!provider.id || provider.paused === paused) continue;
      try {
        const saved = paused
          ? await pauseProvider(provider.id, { reason: 'Paused from the Hosts page' })
          : await resumeProvider(provider.id);
        providers = providers.map((p) => (p.id === saved.id ? saved : p));
      } catch (cause) {
        toasts.fromError(
          cause,
          paused ? `${provider.name} was not paused` : `${provider.name} was not resumed`,
        );
        return;
      }
    }
    if (paused) {
      toasts.info(
        'New machines paused',
        'Nothing new is bought. Drains, deletes and machines already on their way carry on.',
      );
    } else {
      toasts.success('New machines resumed', 'Machines may be bought again.');
    }
  }

  /* -- join tokens ------------------------------------------------------------
   * Admin only, and fetched here rather than kept in the fleet cache: they
   * change when somebody mints one, not when the fleet moves.
   * ---------------------------------------------------------------------- */

  let tokens = $state<JoinToken[]>([]);
  let tokensLoading = $state(false);
  let tokensError = $state<unknown>(null);
  let tokensReload = $state(0);

  $effect(() => {
    if (!canAdmin) return;
    void tokensReload;
    const controller = new AbortController();
    tokensLoading = true;
    void listJoinTokens(controller.signal)
      .then((result) => {
        // Only the tokens that could still enrol a host. Spent ones are kept by
        // the controller as the record of how each host arrived, and expired
        // ones wait for the next prune; listing either here buries the few
        // that are worth revoking.
        tokens = (result.items ?? []).filter((t) => !t.used_at && t.usable !== false);
        tokensError = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        tokensError = cause;
      })
      .finally(() => (tokensLoading = false));
    return () => controller.abort();
  });

  /* -- actions ----------------------------------------------------------------- */

  let editing = $state<Host | null>(null);
  let editOpen = $state(false);
  let renaming = $state<Host | null>(null);
  let renameOpen = $state(false);
  let sizing = $state<Host | null>(null);
  let sizeOpen = $state(false);
  let profiling = $state<Host | null>(null);
  let profileOpen = $state(false);
  let deleting = $state<Host | null>(null);
  let deleteOpen = $state(false);

  /**
   * Fetch both halves of this page again: the fleet, and the tokens beside it.
   * The token list reports its own progress in place, so the button follows the
   * reconcile -- the slower and more interesting of the two.
   */
  async function refreshPage(): Promise<void> {
    tokensReload += 1;
    machinesReload += 1;
    await fleet.reconcile();
  }

  async function cordon(host: Host, cordoned: boolean): Promise<void> {
    if (!host.id) return;
    const name = host.name || host.id;
    const result = await fleet.optimistic(
      host.id,
      { cordoned },
      () => cordonHost(host.id ?? '', { cordoned }),
      cordoned ? `${name} was not cordoned` : `${name} was not uncordoned`,
    );
    if (result === undefined) return;
    if (cordoned) {
      toasts.info(
        `${name} cordoned`,
        'Its runners keep going and finish their jobs. No new runner will be placed here.',
      );
    } else {
      toasts.success(`${name} uncordoned`, 'The scheduler may place runners here again.');
    }
  }

  /**
   * Lift a throttle by hand. Optimistic like a cordon: the card shows the
   * host on its configured capacity at once, and comes back if the controller
   * refuses. The toast says what the lift does and does not promise, because
   * nothing pins it -- a host still under pressure is stepped down again on
   * its next heartbeat.
   */
  async function liftThrottle(host: Host): Promise<void> {
    if (!host.id) return;
    const name = host.name || host.id;
    const result = await fleet.optimistic(
      host.id,
      { throttle: undefined, throttle_reason: '', effective_capacity: slotsOf(host) },
      () => clearHostThrottle(host.id ?? ''),
      `The throttle on ${name} was not lifted`,
    );
    if (result === undefined) return;
    toasts.success(
      `Throttle lifted on ${name}`,
      'It takes its configured capacity again on the next pass, and its runners get their full CPU allocation on its next heartbeat. If the pressure is still there, that heartbeat throttles it again.',
    );
  }

  function edit(host: Host): void {
    editing = host;
    editOpen = true;
  }

  function rename(host: Host): void {
    renaming = host;
    renameOpen = true;
  }

  function size(host: Host): void {
    sizing = host;
    sizeOpen = true;
  }

  function sizes(host: Host): void {
    profiling = host;
    profileOpen = true;
  }

  function remove(host: Host): void {
    deleting = host;
    deleteOpen = true;
  }
</script>

<PageHeader
  title="Hosts"
  subtitle="The machines that run runners, and how much room each one has left."
  onrefresh={refreshPage}
>
  {#snippet meta()}
    {#if fleet.loaded && hosts.length > 0}
      <p class="summary">
        {pluralise(hosts.length, 'host')} · {healthy} healthy · {inUse} of {capacity} runner slots in
        use{#if hosts.some((h) => h.connection === 'tailcat')}
          · {hosts.filter((h) => h.connection === 'tailcat').length} via Tailcat{/if}
      </p>
    {/if}
  {/snippet}
  <Button variant="secondary" href="/usage?group_by=host">Usage history</Button>
  {#if canAdmin}
    <Button variant="primary" icon={Plus} href="/hosts/new">Add a host</Button>
  {/if}
</PageHeader>

<div class="content">
  <!--
    A failed reconcile once the hosts are on screen is not an error state: the
    cache still holds every host and the stream keeps updating them. Only a
    first load that never landed gets one, as the Overview does.
  -->
  <LoadingBoundary
    loading={fleet.loading && !fleet.loaded}
    error={fleet.loaded ? null : fleet.error}
    empty={fleet.loaded && hosts.length === 0}
    onretry={() => void fleet.reconcile()}
  >
    {#snippet skeleton()}
      <div class="grid">
        {#each [0, 1, 2] as card (card)}
          <div class="card-skeleton">
            <Skeleton width="45%" height="1.25rem" />
            <Skeleton lines={3} />
            <Skeleton height="var(--z-space-2)" radius="full" />
          </div>
        {/each}
      </div>
    {/snippet}

    {#snippet emptyState()}
      <EmptyState
        icon={Server}
        title="No hosts yet"
        description="A host is a machine running the Zoomies agent, and it is where runners are created. The controller can run one itself, or you can enrol another."
      >
        {#if canAdmin}
          <Button variant="primary" icon={Plus} href="/hosts/new">Add a host</Button>
        {:else}
          <p class="need-admin">An administrator can enrol one.</p>
        {/if}
      </EmptyState>
    {/snippet}

    <MetricGrid
      items={[
        {
          label: 'Available host slots',
          value: hosts
            .filter((h) => hostSignals(h).eligible)
            .every((h) => hostSignals(h).free !== null)
            ? String(
                hosts
                  .filter((h) => hostSignals(h).eligible)
                  .reduce((n, h) => n + (hostSignals(h).free ?? 0), 0),
              )
            : NO_VALUE,
          detail: 'Healthy, uncordoned, compatible hosts; runner fit still applies',
        },
        {
          label: 'Hosts reporting healthy',
          value: `${healthy} / ${hosts.length}`,
          detail: 'Live fleet health',
        },
        {
          label: 'Runner slots in use',
          value: `${inUse} / ${capacity}`,
          detail: 'Configured slots across all hosts',
        },
        {
          label: 'Cordoned hosts',
          value: String(hosts.filter((h) => h.cordoned).length),
          detail: 'Excluded from new placements',
        },
        {
          label: 'Hosts at slot capacity',
          value: String(
            hosts.filter((h) => slotsOf(h) > 0 && (h.active_runners ?? 0) >= slotsOf(h)).length,
          ),
          detail: 'Resource limits can restrict placement sooner',
        },
      ]}
    />
    {#if providers.length > 0}
      <MachineBand
        class="machine-band"
        {machines}
        {providers}
        {canOperate}
        onpause={(paused) => void pauseAll(paused)}
      />
    {/if}
    <div class="capacity-map">
      <HostCapacityMap
        {hosts}
        page="hosts"
        onmanage={(host) => {
          const heading = document.getElementById(`host-${host.id}-name`);
          heading?.scrollIntoView({ block: 'center' });
          heading?.focus({ preventScroll: true });
        }}
      />
    </div>
    <h2 class="controls-heading">Host controls and configuration</h2>
    <div class="grid">
      {#each hosts as host (host.id)}
        <HostCard
          {host}
          machine={host.id ? machineByHost.get(host.id) : null}
          {canOperate}
          {canAdmin}
          oncordon={(target, next) => void cordon(target, next)}
          onthrottle={(target) => void liftThrottle(target)}
          oncapacity={size}
          onsizes={sizes}
          onedit={edit}
          onrename={rename}
          ondelete={remove}
        />
      {/each}
    </div>
  </LoadingBoundary>

  {#if canAdmin}
    <section class="panel" aria-labelledby="join-tokens-heading">
      <!--
        No button of its own. "Add a host" is already the page's primary
        action, in the header, and two of the same button on one screen is a
        question about which one is the real one rather than a convenience.
      -->
      <header>
        <div>
          <h2 id="join-tokens-heading">Join tokens</h2>
          <p>
            Each one enrols a single host, then is spent. Adding a host mints one; revoke any that
            were minted and never used.
          </p>
        </div>
      </header>
      <div class="panel-body">
        <LoadingBoundary
          loading={tokensLoading && tokens.length === 0}
          error={tokensError}
          onretry={() => (tokensReload += 1)}
        >
          {#snippet skeleton()}
            <Skeleton lines={3} />
          {/snippet}
          <JoinTokenList {tokens} onrevoked={() => (tokensReload += 1)} />
        </LoadingBoundary>
      </div>
    </section>
  {/if}
</div>

<HostCapacityDialog bind:open={sizeOpen} host={sizing} onclose={() => (sizing = null)} />
<HostRunnerSizesDialog
  bind:open={profileOpen}
  host={profiling}
  onclose={() => (profiling = null)}
/>

<HostRenameDialog bind:open={renameOpen} host={renaming} onclose={() => (renaming = null)} />
<HostLabelsDialog bind:open={editOpen} host={editing} onclose={() => (editing = null)} />
<HostDeleteDialog bind:open={deleteOpen} host={deleting} onclose={() => (deleting = null)} />

<style>
  .capacity-map {
    margin-bottom: var(--z-space-5);
  }
  .content :global(.machine-band) {
    margin-bottom: var(--z-space-5);
  }
  .controls-heading {
    font-size: var(--z-text-sm);
    margin: 0 0 var(--z-space-3);
    font-weight: var(--z-weight-semibold);
  }
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
  }
  .summary {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  /* Cards in a row share a height rather than each stopping where its own
     content runs out. A host with a cordon notice, or one label more than its
     neighbour, otherwise leaves a ragged edge and a band of empty page before
     the next row -- and the whole point of laying hosts out side by side is
     that their figures can be compared across the row. */
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: var(--z-space-4);
    align-items: stretch;
  }
  .card-skeleton {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .panel {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .panel header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .panel header p {
    margin: var(--z-space-1) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .panel-body {
    padding: var(--z-space-3) 0;
  }
  .need-admin {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  @media (max-width: 768px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
</style>
