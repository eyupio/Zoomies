<!--
  One provider: its machines, its settings, and the three ways its records and
  its reality can disagree.

  The tabs are the three questions, in the order they are asked: what is it
  running, what did we tell it to do, and what does not add up. The last is
  fetched only when it is opened -- an ownership sweep is the expensive part of
  answering it, and a page that asked for one every time somebody looked at a
  provider would be a page that made the thing it reports on worse.
-->
<script lang="ts">
  import { Pause, Play, Stethoscope, Trash2 } from '@lucide/svelte';
  import {
    checkProvider,
    deleteProvider,
    getProvider,
    getProviderOrphans,
    listMachines,
    listProviderPairings,
    pauseProvider,
    releaseMachine,
    resumeProvider,
  } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type {
    Machine,
    Provider,
    ProviderCheck,
    ProviderOrphans,
    ProviderPairing,
  } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { router } from '$lib/router';
  import { machineStatus } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import MachineBand from '$lib/providers/MachineBand.svelte';
  import OrphanReview from '$lib/providers/OrphanReview.svelte';
  import PoolsTab from '$lib/providers/PoolsTab.svelte';
  import CheckFindings from '$lib/providers/CheckFindings.svelte';
  import ProviderForm from '$lib/providers/ProviderForm.svelte';

  const id = $derived(router.params.id ?? '');
  const canOperate = $derived(session.can('operator'));
  const canAdmin = $derived(session.can('admin'));

  let provider = $state<Provider | null>(null);
  let machines = $state<Machine[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let showing = '';
  let reload = $state(0);

  let tab = $state(router.param('tab', 'machines'));
  let verdict = $state<ProviderCheck | null>(null);
  let checking = $state(false);
  let pausing = $state(false);
  let deleteOpen = $state(false);
  let editing = $state(false);

  let orphans = $state<ProviderOrphans | null>(null);
  let orphansLoading = $state(false);
  let orphansError = $state<unknown>(null);
  let orphansReload = $state(0);

  /** The result just fetched, or else the one the controller kept from last time. */
  const shownCheck = $derived(verdict ?? provider?.last_check ?? null);
  const changedSinceCheck = $derived(
    provider?.updated_at && provider.last_check_at
      ? new Date(provider.updated_at) > new Date(provider.last_check_at)
      : false,
  );

  const TABS = [
    { id: 'machines', label: 'Machines' },
    { id: 'pools', label: 'Pools' },
    { id: 'settings', label: 'Settings' },
    { id: 'orphans', label: 'Orphan review' },
  ];

  $effect(() => {
    const providerId = id;
    if (providerId === '') return;
    if (showing !== providerId) {
      showing = providerId;
      provider = null;
      machines = [];
      orphans = null;
    }
    void reload;
    const controller = new AbortController();
    loading = true;
    void Promise.all([
      getProvider(providerId, controller.signal),
      listMachines({ provider: providerId, limit: 200 }, controller.signal),
    ])
      .then(([row, page]) => {
        provider = row;
        machines = page.items ?? [];
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  // Every pool against this provider: which it would rent for, and whose
  // setting says no for the rest. Cheap, so fetched with the page.
  let pairings = $state<ProviderPairing[] | null>(null);
  $effect(() => {
    const providerId = id;
    if (providerId === '') return;
    void reload;
    const controller = new AbortController();
    void listProviderPairings(controller.signal)
      .then((page) => {
        pairings = (page.items ?? []).filter((entry) => entry.provider_id === providerId);
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        pairings = [];
      });
    return () => controller.abort();
  });

  // The orphan review is fetched when it is opened, and again only when asked.
  $effect(() => {
    const providerId = id;
    if (providerId === '' || tab !== 'orphans' || !canAdmin) return;
    void orphansReload;
    const controller = new AbortController();
    orphansLoading = true;
    void getProviderOrphans(providerId, controller.signal)
      .then((result) => {
        orphans = result;
        orphansError = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        orphansError = cause;
      })
      .finally(() => (orphansLoading = false));
    return () => controller.abort();
  });

  $effect(() => {
    const providerId = id;
    if (providerId === '') return;
    const stop = [
      events.subscribe('provider.updated', (row) => {
        if (row.id === providerId) provider = row;
      }),
      events.subscribe('machine.updated', (row) => {
        if (row.provider_id !== providerId) return;
        const index = machines.findIndex((m) => m.id === row.id);
        machines = index === -1 ? [...machines, row] : machines.with(index, row);
      }),
      events.subscribe('machine.deleted', (payload) => {
        machines = machines.filter((m) => m.id !== payload.id);
      }),
    ];
    return () => {
      for (const off of stop) off();
    };
  });

  $effect(() => {
    if (provider?.name) router.setTitle(provider.name);
  });

  function selectTab(next: string): void {
    tab = next;
    router.setQuery({ tab: next === 'machines' ? null : next });
  }

  async function check(): Promise<void> {
    if (!provider?.id || checking) return;
    checking = true;
    try {
      verdict = await checkProvider(provider.id);
      reload += 1;
    } catch (cause) {
      toasts.fromError(cause, 'That provider was not checked');
    } finally {
      checking = false;
    }
  }

  async function pause(paused: boolean): Promise<void> {
    if (!provider?.id || pausing) return;
    pausing = true;
    try {
      provider = paused
        ? await pauseProvider(provider.id, { reason: 'Paused from the provider page' })
        : await resumeProvider(provider.id);
      if (paused) {
        toasts.info(
          'New machines paused',
          'Drains, deletes and machines already on their way carry on.',
        );
      } else {
        toasts.success('Resumed', 'Machines may be bought again.');
      }
    } catch (cause) {
      toasts.fromError(cause, paused ? 'It was not paused' : 'It was not resumed');
    } finally {
      pausing = false;
    }
  }

  async function remove(): Promise<boolean> {
    if (!provider?.id) return true;
    try {
      await deleteProvider(provider.id);
      toasts.success(
        `Removed ${provider.name}`,
        'Nothing it owned was touched, because it owned nothing.',
      );
      router.navigate('/providers');
      return true;
    } catch (cause) {
      // A 409 here is the interesting one, and the controller's refusal names
      // how many machines are still live. Showing our own sentence instead
      // would lose the number that decides what to do next.
      toasts.fromError(cause, 'That provider was not removed');
      return false;
    }
  }

  async function release(machine: Machine): Promise<boolean> {
    if (!machine.id || !machine.name) return true;
    try {
      await releaseMachine(machine.id, { name: machine.name });
      toasts.success(`Forgot ${machine.name}`, 'Nothing at the provider was touched.');
      orphansReload += 1;
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, `${machine.name} was not forgotten`);
      return false;
    }
  }

  const settings = $derived(Object.entries(provider?.settings ?? {}));
</script>

{#if loading && !provider}
  <PageHeader title="Provider" breadcrumb={[{ label: 'Providers', href: '/providers' }]} />
  <Skeleton lines={6} />
{:else if error || !provider}
  <PageHeader title="Provider" breadcrumb={[{ label: 'Providers', href: '/providers' }]} />
  <ErrorState {error} title="That provider could not be read" onretry={() => (reload += 1)} />
{:else}
  <!-- One narrowed name for the whole page. Svelte's `{#if}` narrowing does
       not reach inside a snippet body, and every panel below is one. -->
  {@const p = provider}
  <PageHeader
    title={p.name || p.id || 'Provider'}
    breadcrumb={[{ label: 'Providers', href: '/providers' }, { label: p.name ?? '' }]}
    subtitle={p.endpoint}
    onrefresh={async () => {
      reload += 1;
    }}
  >
    {#snippet meta()}
      <p class="summary">
        <Badge tone="neutral" label={p.kind ?? 'unknown'} size="sm" dot={false} />
        {pluralise(p.owned ?? 0, 'machine')} of {p.max_machines ?? 0}
        {#if p.last_sweep_at}
          · swept <RelativeTime value={p.last_sweep_at} plain />{/if}
      </p>
    {/snippet}
    <Button icon={Stethoscope} disabled={!canOperate || checking} onclick={() => void check()}>
      {checking ? 'Checking…' : 'Check'}
    </Button>
    <Button
      icon={p.paused ? Play : Pause}
      disabled={!canOperate || pausing}
      onclick={() => void pause(!p.paused)}
    >
      {#if pausing}
        {p.paused ? 'Resuming…' : 'Pausing…'}
      {:else}
        {p.paused ? 'Resume' : 'Pause new machines'}
      {/if}
    </Button>
    {#if canAdmin}
      <Button variant="danger" icon={Trash2} onclick={() => (deleteOpen = true)}>Remove</Button>
    {/if}
  </PageHeader>

  <div class="content">
    {#if p.held}
      <p class="held"><RemedyText text={p.held} /></p>
    {/if}

    {#if shownCheck}
      <Panel
        title="What the provider said"
        description={verdict
          ? 'Read-only. Nothing was created or changed.'
          : 'The last check, kept so it is still here after a reload. Read-only.'}
      >
        <p class="verdict">
          {#if shownCheck.reachable}
            It answered{#if shownCheck.version}, running {shownCheck.version}{/if}.
          {:else}
            Nothing answered at that address.
          {/if}
          {shownCheck.ok
            ? 'Nothing found stops it being used.'
            : 'Some of what it said needs attention.'}
          Checked <RelativeTime value={shownCheck.checked_at} plain />.
        </p>
        {#if !verdict && changedSinceCheck}
          <p class="stale">
            The settings were changed after this check, so it may no longer be true. Run Check
            again.
          </p>
        {/if}
        <CheckFindings findings={shownCheck.findings} />
      </Panel>
    {/if}

    <Tabs tabs={TABS} value={tab} label="Provider sections" onchange={selectTab}>
      {#snippet children(active)}
        {#if active === 'machines'}
          <div class="tab">
            <MachineBand
              {machines}
              providers={[p]}
              {canOperate}
              onpause={(paused) => void pause(paused)}
            />
            {#if machines.length === 0}
              <EmptyState
                compact
                title="No machines"
                description="Nothing has been rented from this provider. A machine is built when a pool has queued work and no host that can run it."
              />
            {:else}
              <ul class="machines">
                {#each machines as machine (machine.id)}
                  <li>
                    <a href="/machines/{machine.id}">
                      <span class="name">{machine.name}</span>
                      <Badge status={machineStatus(machine.state)} size="sm" />
                      <span class="where">
                        {#if machine.resource_id}{machine.resource_zone} / {machine.resource_id}
                        {:else}no resource yet{/if}
                        {#if machine.host_name}· {machine.host_name}{/if}
                      </span>
                      <RelativeTime value={machine.updated_at} class="when" />
                    </a>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {:else if active === 'pools'}
          <PoolsTab provider={p} {pairings} />
        {:else if active === 'settings'}
          <div class="tab">
            {#if editing && canAdmin}
              <ProviderForm
                provider={p}
                oncancel={() => (editing = false)}
                ondone={(saved) => {
                  provider = saved;
                  editing = false;
                }}
              />
            {:else}
              <dl class="settings">
                <div>
                  <dt>Address</dt>
                  <dd class="mono">{p.endpoint || 'Not set'}</dd>
                </div>
                <div>
                  <dt>Connection</dt>
                  <dd>
                    {p.connection === 'tailcat'
                      ? 'Private, through a zoomies gateway beside the provider. Its address is sealed and never shown.'
                      : 'Direct. Zoomies dials the address over the network.'}
                  </dd>
                </div>
                <div>
                  <dt>Credential</dt>
                  <dd>
                    {p.credentials_configured
                      ? 'Stored, sealed with the instance key. It is never shown again.'
                      : 'None. Nothing can be created until one is stored.'}
                  </dd>
                </div>
                <div>
                  <dt>Certificate</dt>
                  <dd>
                    {p.insecure_skip_verify
                      ? 'Not verified. The credential travels to whatever answers at that address.'
                      : 'Verified against the trusted certificates.'}
                  </dd>
                </div>
                {#each settings as [key, value] (key)}
                  <div>
                    <dt>{key}</dt>
                    <dd class="mono">{value}</dd>
                  </div>
                {/each}
                <div>
                  <dt>Machine</dt>
                  <dd>
                    {pluralise(p.machine_capacity ?? 0, 'runner slot')} ·
                    {p.machine_backend}
                  </dd>
                </div>
                <div>
                  <dt>Pools</dt>
                  <dd>
                    {#if Object.keys(p.pool_selector ?? {}).length === 0}
                      Any pool it suits.
                    {:else}
                      Only pools matching
                      <span class="mono"
                        >{Object.entries(p.pool_selector ?? {})
                          .map(([k, v]) => (v === '' ? k : `${k}=${v}`))
                          .join(', ')}</span
                      >.
                    {/if}
                  </dd>
                </div>
                <div>
                  <dt>Ceiling</dt>
                  <dd>
                    {pluralise(p.max_machines ?? 0, 'machine')}, {p.max_creates_in_flight} built at once
                  </dd>
                </div>
              </dl>
              {#if canAdmin}
                <div class="edit">
                  <Button onclick={() => (editing = true)}>Edit these settings</Button>
                </div>
              {/if}
            {/if}
          </div>
        {:else}
          <div class="tab">
            {#if !canAdmin}
              <ErrorState
                title="Not allowed"
                description="The orphan review needs the administrator role: it lists resources this fleet may be paying for, and the one action on it forgets a row for good."
              />
            {:else if orphansLoading && !orphans}
              <Skeleton lines={5} />
            {:else if orphansError}
              <ErrorState
                error={orphansError}
                title="The review could not be read"
                onretry={() => (orphansReload += 1)}
              />
            {:else if orphans}
              <OrphanReview {orphans} {canAdmin} onrelease={release} />
            {/if}
          </div>
        {/if}
      {/snippet}
    </Tabs>
  </div>

  <ConfirmDialog
    bind:open={deleteOpen}
    title="Remove this provider"
    name={p.name}
    requireName
    confirmLabel="Remove it"
    description="Zoomies forgets where these machines came from. It is refused while the provider still owns any."
    consequences={[
      'The stored credential is destroyed with the row.',
      'Nothing at the provider is touched, now or ever.',
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
  .held {
    margin: 0;
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-draining-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-draining-subtle);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .tab {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-top: var(--z-space-4);
  }
  .verdict {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .machines {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .stale {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .machines {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .machines li + li {
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .machines a {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
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
  .settings {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
  }
  .settings div {
    display: grid;
    grid-template-columns: 14rem minmax(0, 1fr);
    gap: var(--z-space-3);
  }
  dt {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .edit {
    display: flex;
  }
  @media (max-width: 768px) {
    .settings div {
      grid-template-columns: 1fr;
      gap: var(--z-nudge-1);
    }
  }
</style>
