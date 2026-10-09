<!--
  Providers: where machines are rented from, and what each is renting now.

  The page is fetched here rather than kept in the fleet cache, and that is a
  decision rather than an oversight. Providers and machines are read by the two
  pages that show them; putting them in the cache would cost every signed-in
  tab another request on every reconcile, and the shell another slice of a
  budget that has none to spare. The live half is a subscription to the two
  kinds this page cares about, which costs nothing when nobody is looking.
-->
<script lang="ts">
  import { Cloud, Plus } from '@lucide/svelte';
  import {
    checkProvider,
    listMachines,
    listProviders,
    pauseProvider,
    resumeProvider,
  } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Machine, Provider } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { router } from '$lib/router';
  import { machineStatus } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import MetricGrid from '$lib/components/MetricGrid.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import MachineBand from '$lib/providers/MachineBand.svelte';
  import ProviderCard from '$lib/providers/ProviderCard.svelte';
  import { machinesNeedingReview } from '$lib/providers/machines';
  import { MACHINE_STATES } from '$lib/api/types';

  const canOperate = $derived(session.can('operator'));
  const canAdmin = $derived(session.can('admin'));

  let providers = $state<Provider[]>([]);
  let machines = $state<Machine[]>([]);
  let loading = $state(true);
  let loaded = $state(false);
  let error = $state<unknown>(null);
  let reload = $state(0);
  let checking = $state<string>('');
  let pausing = $state<string>('');

  /** The state filter lives in the URL, so the band's links land on a view. */
  const stateFilter = $derived(router.param('state'));
  const shown = $derived(stateFilter ? machines.filter((m) => m.state === stateFilter) : machines);
  const review = $derived(machinesNeedingReview(machines));

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void Promise.all([
      listProviders(controller.signal),
      // Deleted machines are history, and a fleet that has been running a
      // month has hundreds of them. The orphan review is where a confirmed
      // deletion is still worth looking at.
      listMachines({ limit: 200 }, controller.signal),
    ])
      .then(([providerPage, machinePage]) => {
        providers = providerPage.items ?? [];
        machines = machinePage.items ?? [];
        error = null;
        loaded = true;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  // Live, without a collection in the fleet cache: one frame in, one row
  // replaced. A machine the fleet has not seen before is appended, because the
  // first frame about a machine is the one that says it was planned.
  $effect(() => {
    const stop = [
      events.subscribe('provider.updated', (row) => {
        providers = providers.map((p) => (p.id === row.id ? row : p));
      }),
      events.subscribe('machine.updated', (row) => {
        const index = machines.findIndex((m) => m.id === row.id);
        machines = index === -1 ? [...machines, row] : machines.with(index, row);
      }),
      events.subscribe('machine.deleted', (payload) => {
        machines = machines.filter((m) => m.id !== payload.id);
      }),
      events.subscribe('provider.deleted', (payload) => {
        providers = providers.filter((p) => p.id !== payload.id);
      }),
    ];
    return () => {
      for (const off of stop) off();
    };
  });

  async function refreshPage(): Promise<void> {
    reload += 1;
  }

  async function check(provider: Provider): Promise<void> {
    if (!provider.id) return;
    checking = provider.id;
    try {
      const result = await checkProvider(provider.id);
      if (result.ok) {
        toasts.success(
          `${provider.name} answered`,
          result.version ? `Running ${result.version}.` : 'Nothing found that stops it being used.',
        );
      } else {
        toasts.info(
          `${provider.name} has ${pluralise(result.findings?.length ?? 0, 'finding')}`,
          'Open the provider to read them.',
        );
      }
      reload += 1;
    } catch (cause) {
      toasts.fromError(cause, `${provider.name} was not checked`);
    } finally {
      checking = '';
    }
  }

  async function pause(provider: Provider, paused: boolean): Promise<void> {
    if (!provider.id) return;
    pausing = provider.id;
    try {
      const saved = paused
        ? await pauseProvider(provider.id, { reason: 'Paused from the Providers page' })
        : await resumeProvider(provider.id);
      providers = providers.map((p) => (p.id === saved.id ? saved : p));
      if (paused) {
        toasts.info(
          `${provider.name} paused`,
          'Nothing new is bought. Drains, deletes and machines already on their way carry on.',
        );
      } else {
        toasts.success(`${provider.name} resumed`, 'Machines may be bought again.');
      }
    } catch (cause) {
      toasts.fromError(
        cause,
        paused ? 'That provider was not paused' : 'That provider was not resumed',
      );
    } finally {
      pausing = '';
    }
  }

  async function pauseAll(paused: boolean): Promise<void> {
    for (const provider of providers) {
      if (provider.paused === paused) continue;
      await pause(provider, paused);
    }
  }
</script>

<PageHeader
  title="Providers"
  subtitle="Where machines are rented from. A machine becomes a host when its agent joins, and is deleted when the work runs out."
  onrefresh={refreshPage}
>
  {#snippet meta()}
    {#if loaded && providers.length > 0}
      <p class="summary">
        {pluralise(providers.length, 'provider')} · {pluralise(machines.length, 'machine')}
        {#if review.length > 0}
          · {review.length} needing review{/if}
      </p>
    {/if}
  {/snippet}
  {#if canAdmin}
    <Button variant="primary" icon={Plus} href="/providers/new">Add a provider</Button>
  {/if}
</PageHeader>

<div class="content">
  <LoadingBoundary
    class="stack"
    loading={loading && !loaded}
    error={loaded ? null : error}
    empty={loaded && providers.length === 0}
    onretry={() => (reload += 1)}
  >
    {#snippet skeleton()}
      <div class="grid">
        {#each [0, 1] as card (card)}
          <div class="card-skeleton">
            <Skeleton width="45%" height="1.25rem" />
            <Skeleton lines={3} />
          </div>
        {/each}
      </div>
    {/snippet}

    {#snippet emptyState()}
      <EmptyState
        icon={Cloud}
        title="No providers yet"
        description="A provider is somewhere Zoomies may rent a machine, a hypervisor, say. It builds one when a pool has work and nowhere to put it, and deletes it again when the work runs out. Nothing is rented until you raise a ceiling above zero."
      >
        {#if canAdmin}
          <Button variant="primary" icon={Plus} href="/providers/new">Add a provider</Button>
        {:else}
          <p class="need-admin">An administrator can add one.</p>
        {/if}
      </EmptyState>
    {/snippet}

    <MetricGrid
      items={[
        {
          label: 'Machines owned',
          value: String(providers.reduce((n, p) => n + (p.owned ?? 0), 0)),
          detail: 'Holding a resource somebody is being billed for',
        },
        {
          label: 'Ceiling',
          value: String(providers.reduce((n, p) => n + (p.max_machines ?? 0), 0)),
          detail: 'The most that may exist at once',
        },
        {
          label: 'Needing review',
          value: String(review.length),
          detail: 'Nothing here deletes one of these on its own',
          tone: review.length > 0 ? 'danger' : 'neutral',
        },
        {
          label: 'Paused providers',
          value: String(providers.filter((p) => p.paused).length),
          detail: 'New machines held; drains and deletes continue',
        },
      ]}
    />

    <MachineBand {machines} {providers} {canOperate} onpause={(paused) => void pauseAll(paused)} />

    <div class="grid">
      {#each providers as provider (provider.id)}
        <ProviderCard
          {provider}
          {canOperate}
          checking={checking === provider.id}
          pausing={pausing === provider.id}
          oncheck={(target) => void check(target)}
          onpause={(target, next) => void pause(target, next)}
        />
      {/each}
    </div>

    <section class="panel" aria-labelledby="machines-heading">
      <header>
        <div>
          <h2 id="machines-heading">Machines</h2>
          <p>
            Every machine across every provider. A machine exists because a pool's unmet demand
            asked for one; there is no way to make one by hand, because a second source of supply
            would be a second thing the reconciler would then decide to delete.
          </p>
        </div>
        <Select
          value={stateFilter}
          ariaLabel="Filter machines by state"
          placeholder="Any state"
          size="sm"
          options={MACHINE_STATES.map((state) => ({
            value: state,
            label: machineStatus(state).label,
          }))}
          onchange={(next) => router.setQuery({ state: next || null })}
        />
      </header>
      {#if shown.length === 0}
        <div class="panel-body">
          <EmptyState
            compact
            title="No machines"
            description={stateFilter
              ? 'No machine is in that state right now.'
              : 'Nothing has been rented yet.'}
          />
        </div>
      {:else}
        <ul class="machines">
          {#each shown as machine (machine.id)}
            <li>
              <a href="/machines/{machine.id}">
                <span class="name">{machine.name}</span>
                <Badge status={machineStatus(machine.state)} size="sm" />
                <span class="where">
                  {machine.provider_name || machine.provider_id}
                  {#if machine.resource_id}· {machine.resource_zone} / {machine.resource_id}{/if}
                </span>
                <RelativeTime value={machine.updated_at} class="when" />
              </a>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  </LoadingBoundary>
</div>

<style>
  /* The gap has to sit on LoadingBoundary's own wrapper: `.content` only has
     that one child, so a gap on it spaces nothing and the metrics, the band,
     the cards and the Machines panel all touch. */
  .content :global(.stack) {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .content :global(.stack > .metrics) {
    margin-bottom: 0;
  }
  .summary {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
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
    padding: var(--z-space-4);
  }
  .machines {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .machines li + li {
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .machines a {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-5);
    color: var(--z-text);
    text-decoration: none;
  }
  .machines a:hover {
    background: var(--z-surface-hover);
  }
  .name {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    overflow-wrap: anywhere;
  }
  .where {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .machines :global(.when) {
    margin-left: auto;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
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
    .panel header {
      flex-direction: column;
    }
  }
</style>
