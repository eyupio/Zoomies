<!--
  What the fleet knows about one repository: the jobs it ran for it, how they
  ended, how long they waited and took, and which pools and hosts ran them.

  None of it is read from GitHub. It is the fleet's own record, asked of routes
  that already exist (`GET /jobs/stats` and `GET /jobs`), so it works whatever
  Kennel Club could or could not read, and it works for a repository nobody is
  tracking. Jobs on somebody else's hosted runners are left out: the fleet did
  not run them.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { getJobStats, listJobs } from '$lib/api/client';
  import type { JobStats, KennelRepository } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import MetricGrid from '$lib/components/MetricGrid.svelte';
  import type { Metric } from '$lib/components/MetricGrid.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { formatPercent, pluralise } from '$lib/format';
  import {
    DEFAULT_OVERVIEW_WINDOW,
    OVERVIEW_WINDOWS,
    outcomeText,
    partialWindowNote,
    percentileTile,
    placeRows,
    successRateText,
    waitingText,
    windowStart,
    type OverviewWindow,
  } from '$lib/kennel/runtime';
  import { openFindingsText, TRACKING_OVERVIEW, worstSeverity } from '$lib/kennel/words';
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs } from '$lib/state/prefs.svelte';
  import { kennelStatus, kennelTrackingStatus } from '$lib/status';

  interface Props {
    repo: KennelRepository;
    /**
     * Moves when the page reloads itself -- its Refresh button, or the stream
     * coming back -- so that one pass refreshes everything on it. The page
     * listens for the stream; listening here as well would ask twice.
     */
    refreshKey?: number;
  }

  let { repo, refreshKey = 0 }: Props = $props();

  let windowId = $state<OverviewWindow>(DEFAULT_OVERVIEW_WINDOW);
  const days = $derived(OVERVIEW_WINDOWS.find((w) => w.id === windowId)?.days ?? 7);

  let totals = $state<JobStats | null>(null);
  let pools = $state<JobStats | null>(null);
  let hosts = $state<JobStats | null>(null);
  let waiting = $state<number | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);
  let showing = '';
  // A derived string only changes when the name does. Reading `repo.name` in the
  // effect would also depend on the object, and the page replaces that on every
  // reload: each Refresh would then ask the jobs API twice.
  const name = $derived(repo.name);

  $effect(() => {
    void reload;
    void refreshKey;
    const span = days;
    // What was drawn for one repository or window must not outlive a change to
    // either: a table of last week's jobs under "30 days" is a wrong number.
    const key = `${name}\u0000${span}`;
    if (untrack(() => showing) !== key) {
      showing = key;
      totals = pools = hosts = null;
      waiting = null;
      error = null;
    }
    const controller = new AbortController();
    loading = true;
    const base = { repo: [name], hosted: false, since: windowStart(span, new Date()) };
    void Promise.all([
      getJobStats(base, controller.signal),
      getJobStats({ ...base, group_by: ['pool'] }, controller.signal),
      getJobStats({ ...base, group_by: ['host'] }, controller.signal),
      listJobs({ repo: [name], unmatched: true, limit: 1 }, controller.signal),
    ])
      .then(([all, byPool, byHost, queued]) => {
        totals = all;
        pools = byPool;
        hosts = byHost;
        waiting = queued.total;
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => {
        if (!controller.signal.aborted) loading = false;
      });
    return () => controller.abort();
  });

  const group = $derived(totals?.groups[0]);
  const finished = $derived(group?.count ?? 0);
  const poolRows = $derived(placeRows(pools?.groups ?? [], 'pool', (id) => fleet.pool(id)?.name));
  const hostRows = $derived(placeRows(hosts?.groups ?? [], 'host', (id) => fleet.host(id)?.name));

  const queueWait = $derived(percentileTile(group?.queue_wait));
  const duration = $derived(percentileTile(group?.duration));
  // What the duration leaves out is the API's own list of conclusions, said in
  // words, so a tile never claims to time a job it did not.
  const durationLeaves = $derived(
    (totals?.duration_excludes ?? []).length > 0
      ? `leaves out ${(totals?.duration_excludes ?? []).join(' and ')} jobs`
      : '',
  );

  const tiles = $derived<Metric[]>([
    {
      label: 'Jobs finished',
      value: String(finished),
      detail: outcomeText(group),
      tone: 'neutral',
    },
    {
      label: 'Success rate',
      value: successRateText(group),
      detail: 'Of jobs that had a result. Cancelled and skipped jobs are not counted.',
      tone: 'neutral',
    },
    {
      label: 'Time waiting for a runner',
      value: queueWait.value,
      detail: queueWait.detail,
      tone: 'neutral',
    },
    {
      label: 'Time running',
      value: duration.value,
      detail: durationLeaves ? `${duration.detail}; ${durationLeaves}` : duration.detail,
      tone: 'neutral',
    },
  ]);

  const note = $derived(partialWindowNote(days));
  const tracked = $derived(repo.tracking.tracked);
  // Stopping resets the row to pending with nothing found, so a standing read from it
  // would be a standing nobody gave.
  const status = $derived(
    tracked
      ? kennelStatus(repo.state, worstSeverity(repo.counts), prefs.quirkyStatus)
      : kennelTrackingStatus(),
  );
</script>

<div class="overview">
  <div class="toolbar">
    <Segmented
      options={OVERVIEW_WINDOWS.map((w) => ({ value: w.id, label: w.label }))}
      value={windowId}
      label="Time window"
      onchange={(value) => (windowId = value as OverviewWindow)}
    />
    {#if note}<p class="note">{note}</p>{/if}
  </div>

  {#if loading && !totals}
    <Skeleton lines={6} />
  {:else if error && !totals}
    <ErrorState
      {error}
      title="What the fleet knows could not be read"
      onretry={() => (reload += 1)}
    />
  {:else if totals}
    {#if finished === 0}
      <EmptyState
        compact
        title="No jobs finished in the last {days} days"
        description="The fleet ran none for this repository in that time, or they are older than the fleet keeps."
      >
        {#if windowId !== '30d'}
          <Button onclick={() => (windowId = '30d')}>Look at 30 days</Button>
        {/if}
      </EmptyState>
    {:else}
      <MetricGrid items={tiles} />

      <Panel
        title="Where it ran"
        description="The pools and hosts that ran this repository's jobs in the window, busiest first."
      >
        <div class="places">
          <section aria-labelledby="pools-heading">
            <h3 id="pools-heading">Pools</h3>
            {#if poolRows.length === 0}
              <p class="note">None recorded.</p>
            {:else}
              <ul>
                {#each poolRows as row (row.id)}
                  <li>
                    {#if row.known}
                      <a href="/pools/{encodeURIComponent(row.id)}">{row.name}</a>
                    {:else}
                      <span class="gone" title="The fleet no longer has this pool">{row.name}</span>
                    {/if}
                    <span class="count"
                      >{pluralise(row.jobs, 'job')}, {formatPercent(row.share)}</span
                    >
                  </li>
                {/each}
              </ul>
            {/if}
          </section>
          <section aria-labelledby="hosts-heading">
            <h3 id="hosts-heading">Hosts</h3>
            {#if hostRows.length === 0}
              <p class="note">None recorded.</p>
            {:else}
              <ul>
                {#each hostRows as row (row.id || 'none')}
                  <li>
                    {#if row.known}
                      <a href="/hosts/{encodeURIComponent(row.id)}">{row.name}</a>
                    {:else}
                      <span class="gone" title="The fleet has no record of this host for these jobs"
                        >{row.name}</span
                      >
                    {/if}
                    <span class="count"
                      >{pluralise(row.jobs, 'job')}, {formatPercent(row.share)}</span
                    >
                  </li>
                {/each}
              </ul>
            {/if}
          </section>
        </div>
      </Panel>
    {/if}

    <Panel
      title="Waiting now"
      description="Jobs queued for this repository that no pool will take."
    >
      <p class="waiting">
        {waitingText(waiting ?? 0)}
        {#if (waiting ?? 0) > 0}
          <a href="/queue?repo={encodeURIComponent(repo.name)}&unmatched=true"
            >See them in the queue</a
          >
        {/if}
      </p>
    </Panel>
  {/if}

  <Panel
    title="What Kennel Club says"
    description={tracked ? 'Its conclusions are on the CI tab.' : TRACKING_OVERVIEW.description}
  >
    <p class="standing">
      <Badge {status} size="sm" />
      {#if tracked}
        <span>{openFindingsText(repo.counts)}</span>
        <a href="/kennel/repositories/{encodeURIComponent(repo.id)}/ci">Open the CI tab</a>
      {:else}
        <span>{TRACKING_OVERVIEW.detail}</span>
      {/if}
    </p>
  </Panel>
</div>

<style>
  .overview {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .toolbar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
  .note {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .places {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
    gap: var(--z-space-5);
  }
  h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: var(--z-space-3);
    font-size: var(--z-text-sm);
    min-width: 0;
  }
  li a,
  .gone {
    overflow-wrap: anywhere;
  }
  .gone {
    color: var(--z-text-muted);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
  }
  .count {
    flex: none;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    font-variant-numeric: tabular-nums;
  }
  .waiting,
  .standing {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
</style>
