<!--
  One runner, for the moment something has gone wrong with it.

  The order of the page is the order of the questions: what state is it in and
  who owns it, what is it running, how did it get here, what is it made of, and
  then the log -- which is where an operator spends the rest of their time, so
  it gets the rest of the page.

  Nothing here polls. The detail arrives once, and after that the runner's own
  SSE updates are merged into it, so the badge, the job and the resource figures
  move on their own. The timeline is refetched when, and only when, the state
  actually changes.
-->
<script lang="ts">
  import AskEli from '$lib/assistant/AskEli.svelte';
  import RunnerInsights from '$lib/insights/RunnerInsights.svelte';
  import { CircleSlash, TriangleAlert, Trash2, Unplug } from '@lucide/svelte';
  import { ApiError, getRunner, getRunnerTimeline } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { RunnerDetail, TimelineEntry } from '$lib/api/types';
  import { faultLabel } from '$lib/faults';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import LogViewer from '$lib/logs/LogViewer.svelte';
  import RunnerConfirm from '$lib/runners/RunnerConfirm.svelte';
  import RunnerFacts from '$lib/runners/RunnerFacts.svelte';
  import MemoryBadgeCard from '$lib/runners/MemoryBadgeCard.svelte';
  import RunnerMemoryBadges from '$lib/runners/RunnerMemoryBadges.svelte';
  import { memoryBadges } from '$lib/runners/memory-badges';
  import RunnerJob from '$lib/runners/RunnerJob.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RunnerResources from '$lib/runners/RunnerResources.svelte';
  import RunnerTimeline from '$lib/runners/RunnerTimeline.svelte';
  import RunnerStatus from '$lib/runners/RunnerStatus.svelte';

  const id = $derived(router.params.id ?? '');
  const canOperate = $derived(session.can('operator'));

  let runner = $state<RunnerDetail | null>(null);
  let timeline = $state<TimelineEntry[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let gone = $state(false);
  /** Which runner and state the timeline on screen belongs to. */
  let timelineFor = '';
  /** Which runner the page is showing, so moving to another one starts clean. */
  let showing = '';

  /* -- loading --------------------------------------------------------------- */

  async function load(runnerId: string, signal: AbortSignal): Promise<void> {
    loading = true;
    try {
      const detail = await getRunner(runnerId, signal);
      if (signal.aborted) return;
      runner = detail;
      timeline = detail.timeline ?? [];
      timelineFor = `${runnerId}:${detail.state ?? ''}`;
      error = null;
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return;
      error = cause;
    } finally {
      if (!signal.aborted) loading = false;
    }
  }

  $effect(() => {
    const runnerId = id;
    if (runnerId === '') return;
    // Moving from one runner to another must not leave the previous one's
    // details on screen while the next request is in flight.
    if (showing !== runnerId) {
      showing = runnerId;
      runner = null;
      timeline = [];
    }
    const controller = new AbortController();
    gone = false;
    void load(runnerId, controller.signal);
    return () => controller.abort();
  });

  // Live updates for this runner alone. The fleet cache has them too, but the
  // detail response carries the host, the pool and the timeline with it, so the
  // page merges rather than replaces.
  $effect(() => {
    const runnerId = id;
    if (runnerId === '') return;
    const stop = [
      events.subscribe(['runner.created', 'runner.updated'], (row) => {
        if (row.id !== runnerId || !runner) return;
        // A frame is the runner's whole view, and a view leaves out what a runner
        // has none of: spreading it over the page's copy would keep a loan, or a
        // list of folders, that the runner no longer has -- the card for a valve
        // that was switched off would stay until the page was reloaded.
        const { memory_resource: _memory, scratch: _scratch, ...kept } = runner;
        runner = { ...kept, ...row };
      }),
      events.subscribe('runner.deleted', (payload) => {
        if (payload.id === runnerId) gone = true;
      }),
    ];
    return () => {
      for (const off of stop) off();
    };
  });

  // The timeline changes only when the state does, so that is what it watches.
  $effect(() => {
    const runnerId = id;
    const current = runner?.state;
    if (runnerId === '' || !current) return;
    const key = `${runnerId}:${current}`;
    if (key === timelineFor) return;
    timelineFor = key;
    const controller = new AbortController();
    void (async () => {
      try {
        const result = await getRunnerTimeline(runnerId, controller.signal);
        if (!controller.signal.aborted) timeline = result.items ?? [];
      } catch {
        // The timeline is context, not the point of the page. Keeping the last
        // one is better than replacing it with an error.
      }
    })();
    return () => controller.abort();
  });

  $effect(() => {
    if (runner?.name) router.setTitle(runner.name);
  });

  /* -- what the header shows -------------------------------------------------- */

  // The cached runner first: draining flips the badge before the request that
  // caused it has come back. (A local called `state` would collide with the
  // `$state` rune, hence the name.)
  const liveState = $derived(fleet.runner(id)?.state ?? runner?.state);
  const terminal = $derived(liveState === 'removed' || liveState === 'failed');
  const running = $derived(!runner?.finished_at);
  const failureMessage = $derived(liveState === 'failed' ? (runner?.message ?? '') : '');
  // What the memory valve has done and which folders are in memory, as the same
  // cards the badges open, so the page says what the tooltip says.
  const memoryCards = $derived(runner ? memoryBadges(runner, { quiet: true }) : []);

  const logsReason = $derived.by(() => {
    if (liveState === 'removed') {
      return 'This runner is gone, so there is no container left to read. Its output was not kept after it exited.';
    }
    const host = runner?.host?.name ?? runner?.host_name;
    if (host) {
      return `${host} has not checked in recently, so its agent cannot relay this runner's output. Check the agent on that host.`;
    }
    return "Zoomies reads a runner's output through the agent on its host, and that host is not reachable right now.";
  });

  /* -- actions ----------------------------------------------------------------- */

  let confirmOpen = $state(false);
  let confirmAction = $state<'drain' | 'delete'>('drain');

  function ask(action: 'drain' | 'delete'): void {
    confirmAction = action;
    confirmOpen = true;
  }

  const targets = $derived(runner ? [runner] : []);

  /**
   * Deleting takes the runner away from under this page, so there is nothing
   * left here to look at -- back to wherever the operator came from, the
   * runners grid if this page was opened directly. Draining leaves the runner
   * in place, still working, so that stays on the page.
   */
  function onRunnerConfirmed(): void {
    if (confirmAction === 'delete') router.back('/runners');
  }

  /**
   * Fetch this runner again. The detail response carries the host, the pool and
   * the timeline with it, so one request answers for the whole page -- and for
   * a runner that has gone since, the 404 is the honest answer to "is it still
   * there?", which is usually why somebody pressed it.
   */
  function refreshPage(): Promise<void> {
    if (id === '') return Promise.resolve();
    return load(id, new AbortController().signal);
  }
</script>

<PageHeader
  title={runner?.name ??
    (error instanceof ApiError && error.isNotFound ? 'Runner not found' : 'Runner')}
  breadcrumb={[{ label: 'Runners', href: '/runners' }, { label: runner?.name ?? 'Runner' }]}
  onrefresh={refreshPage}
>
  {#snippet meta()}
    {#if runner}
      <RunnerStatus runner={fleet.runner(id) ?? runner} />
      <!-- The page's own copy, which the runner's events are merged into: it is
           the freshest there is on first load, where the cache may hold a row
           from some time ago. -->
      <RunnerMemoryBadges {runner} quiet />
      {#if runner.pool_name}
        <span class="crumb">
          in <a href="/pools/{runner.pool_id}">{runner.pool_name}</a>
        </span>
      {/if}
      {#if runner.host_name}
        <span class="crumb">on <a href="/hosts">{runner.host_name}</a></span>
      {/if}
      <span class="crumb"><RelativeTime value={runner.created_at} prefix="created" /></span>
    {/if}
  {/snippet}

  {#if runner && canOperate}
    <Button
      variant="secondary"
      icon={CircleSlash}
      disabled={terminal || liveState === 'draining'}
      title={terminal
        ? 'This runner has already finished.'
        : 'Finish the current job, then exit. Nothing is interrupted.'}
      onclick={() => ask('drain')}
    >
      Drain
    </Button>
    <Button variant="danger" icon={Trash2} onclick={() => ask('delete')}>Delete</Button>
  {/if}
  {#if runner}
    <AskEli
      context={{
        kind: 'runner',
        title: runner.name || runner.id || 'Runner',
        facts: {
          'Runner ID': runner.id,
          State: liveState,
          Pool: runner.pool_name,
          Host: runner.host_name,
          Failure: failureMessage || runner.message,
        },
        question:
          liveState === 'failed'
            ? 'Help me diagnose this runner failure and plan a safe recovery.'
            : 'Explain this runner state and what I should check next.',
      }}
    />
  {/if}
</PageHeader>

{#if gone}
  <p class="callout neutral">
    <TriangleAlert size={15} aria-hidden="true" />
    <span>
      This runner has been removed. What is on this page is the last thing the controller knew about
      it.
    </span>
  </p>
{/if}

{#if failureMessage}
  <!--
    A failed runner says what went wrong and, since the categories, what to do
    about it. A runner that never took a job is the case this matters most for:
    nothing else in the system says why a pool's containers will not start,
    because the jobs it was meant for are still queued and none of them is
    marked failed.
  -->
  <div class="callout danger stacked">
    <TriangleAlert size={15} aria-hidden="true" />
    <div class="callout-body">
      {#if runner?.fault_kind}
        <p class="callout-heading">{faultLabel(runner.fault_kind)}</p>
      {/if}
      <p>{failureMessage}</p>
      {#if runner?.fault_fix}
        <p class="callout-fix"><span class="callout-fix-label">Fix</span> {runner.fault_fix}</p>
      {/if}
    </div>
  </div>
{/if}

<!--
  A cleanup failure is a different thing from a failed runner, and it is shown
  even on a removed one -- especially on a removed one. A failed runner is a job
  that did not run; this is something still on a host or on somebody's
  organisation, and the row it belongs to is the only place it is written down.
  Zoomies keeps retrying, so the wording says that rather than asking for an
  action that may already be unnecessary.
-->
{#if runner?.cleanup_error}
  <p class="callout pending">
    <TriangleAlert size={15} aria-hidden="true" />
    <span>
      Zoomies could not finish taking this runner away{runner.cleanup_attempts
        ? ` after ${runner.cleanup_attempts} ${runner.cleanup_attempts === 1 ? 'attempt' : 'attempts'}`
        : ''}: {runner.cleanup_error}. Something is left behind, a container on its host, or a
      registration on GitHub. Zoomies keeps retrying, and this clears when it succeeds.
    </span>
  </p>
{/if}

{#if error}
  <ErrorState
    {error}
    title="That runner could not be loaded"
    onretry={() => {
      const controller = new AbortController();
      void load(id, controller.signal);
    }}
  />
{:else if loading && !runner}
  <div class="layout" aria-busy="true">
    <div class="column">
      <Skeleton height="8rem" />
      <Skeleton height="14rem" />
    </div>
    <div class="column">
      <Skeleton height="18rem" />
    </div>
  </div>
  <div class="log-skeleton"><Skeleton height="24rem" /></div>
  <span class="sr-only">Loading this runner</span>
{:else if runner}
  <div class="layout">
    <div class="column">
      <Panel
        title="Current job"
        description="What this runner is working on right now, and the run it belongs to."
      >
        <RunnerJob job={runner.current_job} idle={liveState === 'idle' || liveState === 'busy'} />
      </Panel>

      <Panel
        title="Timeline"
        description="How long it spent in each state. This is a summary of the runner's life rather than an audit trail: a runner that went idle, busy and idle again shows only the most recent of those."
      >
        <RunnerTimeline entries={timeline} {running} />
      </Panel>
    </div>

    <div class="column">
      <Panel title="Details">
        <RunnerFacts {runner} />
      </Panel>

      <Panel title="Resource usage" description="As the host's agent last reported it.">
        {#if runner.cpu_resource}
          <p class="text-sm text-muted-foreground">
            CPU allocation: {runner.cpu_resource.current_cpus ?? 'Not reported'} current ·
            {runner.cpu_resource.guaranteed_cpus ?? 'Not reported'} guaranteed ·
            {runner.cpu_resource.ceiling_cpus ?? 'Not reported'} ceiling
          </p>
          {#if runner.cpu_resource.sized_for_cpus}
            <p class="text-sm text-muted-foreground">
              Builds sized for {runner.cpu_resource.sized_for_cpus} CPUs: Cargo, .NET and the JVM were
              started with workers for the ceiling, so a boost that arrives mid-job can be used.
            </p>
          {/if}
        {/if}
        {#if runner.resource_sample?.sampled_at}
          <p class="text-sm text-muted-foreground">
            Last successful sample: {new Date(runner.resource_sample.sampled_at).toLocaleString()}.
            Values may be retained if sampling fails.
          </p>
        {:else}
          <p class="text-sm text-muted-foreground">Sample freshness is unknown.</p>
        {/if}
        {#if runner.resource_sample?.cpu_throttling}
          <p class="text-sm text-muted-foreground">
            CPU quota throttled {runner.resource_sample.cpu_throttling.throttled_periods ?? 0} of {runner
              .resource_sample.cpu_throttling.periods ?? 0} periods, totalling {(
              (runner.resource_sample.cpu_throttling.throttled_nanoseconds ?? 0) / 1e9
            ).toFixed(2)} seconds since this workload started.
          </p>
        {/if}
        <RunnerResources
          cpuPercent={runner.cpu_percent}
          memoryBytes={runner.memory_bytes}
          limits={runner.pool?.resources}
          allocatedCpus={runner.allocated_cpus}
          allocatedMemoryMb={runner.allocated_memory_mb}
          allocationSource={runner.allocation_source}
          hostThrottle={(fleet.host(runner.host_id) ?? runner.host)?.throttle}
          backend={(runner.pool ?? fleet.pool(runner.pool_id))?.backend}
          lentMemoryMb={runner.memory_resource?.lent_mb}
        />
      </Panel>

      {#if memoryCards.length > 0}
        <Panel
          title="Memory and folders"
          description="What the memory valve has done for this runner, and which of its folders are kept in memory."
        >
          <div class="memory-cards" data-testid="memory-cards">
            {#each memoryCards as badge (badge.key)}
              <div class="memory-card"><MemoryBadgeCard {badge} /></div>
            {/each}
          </div>
        </Panel>
      {/if}
    </div>
  </div>

  <RunnerInsights poolId={runner.pool_id ?? ''} compact />
  <section class="logs" aria-labelledby="runner-log-heading">
    <div class="logs-head">
      <h2 id="runner-log-heading">Log</h2>
      <p>
        {runner.logs_available === false
          ? "A runner's output is relayed by the agent on its host, not read from the controller."
          : `The container's output, relayed live by the agent on ${runner.host_name ?? 'its host'}.`}
      </p>
    </div>

    {#if runner.logs_available === false}
      <div class="unavailable">
        <EmptyState icon={Unplug} title="The log cannot be read right now" description={logsReason}>
          {#if liveState !== 'removed'}
            <Button variant="secondary" href="/hosts">Check the hosts</Button>
          {/if}
        </EmptyState>
      </div>
    {:else}
      <!-- Keyed on the runner: this page stays mounted when the operator moves
           from one runner to the next, and a terminal still tailing the old
           one would be worse than a fresh one. -->
      {#key id}
        <div class="log-frame">
          <LogViewer runnerId={id} runnerName={runner.name} />
        </div>
      {/key}
    {/if}
  </section>
{/if}

<RunnerConfirm
  bind:open={confirmOpen}
  action={confirmAction}
  {targets}
  ondone={onRunnerConfirmed}
/>

<style>
  /* One card per thing there is to say, with a rule between them. */
  .memory-cards {
    display: grid;
    gap: var(--z-space-4);
  }
  .memory-card + .memory-card {
    padding-top: var(--z-space-4);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .stacked {
    align-items: flex-start;
  }
  .callout-body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .callout-body p {
    margin: 0;
  }
  .callout-heading {
    font-weight: var(--z-weight-semibold);
  }
  .callout-fix-label {
    font-weight: var(--z-weight-semibold);
  }
  .crumb {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .crumb a {
    color: var(--z-accent);
    text-decoration: none;
  }
  .crumb a:hover {
    text-decoration: underline;
  }
  .callout {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
  .callout.danger {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
    color: var(--z-danger);
  }
  .callout.neutral {
    border-color: var(--z-neutral-border);
    background: var(--z-neutral-subtle);
    color: var(--z-text-muted);
  }
  /* Pending rather than danger: something is left behind, but Zoomies is still
     retrying and it usually clears itself. Red here would compete with a real
     failure on the same page. */
  .callout.pending {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
    color: var(--z-text);
  }
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
    gap: var(--z-space-4);
    align-items: start;
  }
  .column {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    min-width: 0;
  }
  .log-skeleton {
    margin-top: var(--z-space-6);
  }
  .logs {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin-top: var(--z-space-6);
  }
  .logs-head h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .logs-head p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  /* Tall enough to be worth reading, capped so the page never becomes one
     enormous terminal on a large monitor. */
  .log-frame {
    height: clamp(24rem, 58vh, 52rem);
    min-width: 0;
  }
  .unavailable {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  @media (max-width: 1180px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
