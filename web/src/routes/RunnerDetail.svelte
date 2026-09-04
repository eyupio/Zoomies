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
  import { CircleSlash, TriangleAlert, Trash2, Unplug } from '@lucide/svelte';
  import { getRunner, getRunnerTimeline } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { RunnerDetail, TimelineEntry } from '$lib/api/types';
  import { router } from '$lib/router';
  import { runnerStatus } from '$lib/status';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import LogViewer from '$lib/logs/LogViewer.svelte';
  import RunnerConfirm from '$lib/runners/RunnerConfirm.svelte';
  import RunnerFacts from '$lib/runners/RunnerFacts.svelte';
  import RunnerJob from '$lib/runners/RunnerJob.svelte';
  import RunnerPanel from '$lib/runners/RunnerPanel.svelte';
  import RunnerResources from '$lib/runners/RunnerResources.svelte';
  import RunnerTimeline from '$lib/runners/RunnerTimeline.svelte';

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
        runner = { ...runner, ...row };
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
  const status = $derived(runnerStatus(liveState));
  const terminal = $derived(liveState === 'removed' || liveState === 'failed');
  const running = $derived(!runner?.finished_at);
  const failureMessage = $derived(liveState === 'failed' ? (runner?.message ?? '') : '');

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
</script>

<PageHeader
  title={runner?.name ?? (loading ? 'Runner' : 'Runner not found')}
  breadcrumb={[{ label: 'Runners', href: '/runners' }, { label: runner?.name ?? 'Runner' }]}
>
  {#snippet meta()}
    {#if runner}
      <Badge {status} />
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
  <p class="callout danger">
    <TriangleAlert size={15} aria-hidden="true" />
    <span>{failureMessage}</span>
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
      <RunnerPanel
        title="Current job"
        description="What this runner is working on right now, and the run it belongs to."
      >
        <RunnerJob job={runner.current_job} idle={liveState === 'idle' || liveState === 'busy'} />
      </RunnerPanel>

      <RunnerPanel
        title="Timeline"
        description="How long it spent in each state. This is a summary of the runner's life rather than an audit trail: a runner that went idle, busy and idle again shows only the most recent of those."
      >
        <RunnerTimeline entries={timeline} {running} />
      </RunnerPanel>
    </div>

    <div class="column">
      <RunnerPanel title="Details">
        <RunnerFacts {runner} />
      </RunnerPanel>

      <RunnerPanel title="Resource usage" description="As the host's agent last reported it.">
        <RunnerResources
          cpuPercent={runner.cpu_percent}
          memoryBytes={runner.memory_bytes}
          limits={runner.pool?.resources}
        />
      </RunnerPanel>
    </div>
  </div>

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

<RunnerConfirm bind:open={confirmOpen} action={confirmAction} {targets} />

<style>
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
    border: 1px solid var(--z-border);
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
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  @media (max-width: 1180px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
