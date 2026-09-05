<!--
  What happened to one job, in order, in sentences.

  This is the answer to "what happened to my job?" without a log to read: GitHub
  queued it, a pool claimed it (or nothing did), a runner picked it up, it
  finished -- and, when the fleet is to blame, the runner stopped under it. It
  is fetched when the drawer opens and again on every `job.updated` frame for
  this job, because every change to the timeline arrives with one of those.
-->
<script lang="ts">
  import { getJobEvents } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { JobEvent } from '$lib/api/types';
  import { jobEventStatus } from '$lib/status';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';

  interface Props {
    jobId: string;
    /** The job's conclusion, so the last mark takes its colour. */
    conclusion?: string | null;
    class?: string;
  }

  let { jobId, conclusion = null, class: className = '' }: Props = $props();

  let entries = $state.raw<JobEvent[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  /** Which job the entries on screen belong to, so a new job starts clean. */
  let showing = '';

  async function load(id: string, signal?: AbortSignal): Promise<void> {
    try {
      const result = await getJobEvents(id, signal);
      if (signal?.aborted) return;
      entries = result.items ?? [];
      error = null;
    } catch (cause) {
      if (signal?.aborted) return;
      error = cause;
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  $effect(() => {
    const id = jobId;
    if (!id) return;
    if (showing !== id) {
      showing = id;
      entries = [];
      loading = true;
    }
    const controller = new AbortController();
    void load(id, controller.signal);
    return () => controller.abort();
  });

  // The timeline changes only when the job does, and every such change is
  // announced on the stream for this one job.
  $effect(() => {
    const id = jobId;
    if (!id) return;
    return events.subscribe('job.updated', (job) => {
      if (job.id === id) void load(id);
    });
  });
</script>

{#if error}
  <ErrorState
    {error}
    compact
    title="The timeline could not be loaded"
    onretry={() => void load(jobId)}
  />
{:else if loading}
  <p class="sr-only">Loading the job's timeline.</p>
  <ol class="timeline {className}" aria-hidden="true">
    {#each [0, 1, 2] as row (row)}
      <li>
        <span class="marker"></span>
        <div class="body"><Skeleton width="80%" height="var(--z-text-sm)" /></div>
      </li>
    {/each}
  </ol>
{:else if entries.length === 0}
  <EmptyState
    compact
    title="No history recorded"
    description="The controller writes a line here for everything it sees happen to a job."
  />
{:else}
  <ol class="timeline {className}" aria-label="Timeline">
    {#each entries as entry (entry.id ?? `${entry.kind}-${entry.at}`)}
      {@const status = jobEventStatus(entry.kind, conclusion)}
      <li style="--entry-colour: {status.colour}">
        <span class="marker" aria-hidden="true"><StatusDot {status} size="sm" /></span>
        <div class="body">
          <div class="head">
            <span class="kind">{status.label}</span>
            {#if entry.source}<span class="source">via {entry.source}</span>{/if}
            <RelativeTime value={entry.at} class="when" />
          </div>
          <p class="message">
            {entry.message}
            {#if entry.runner_id && entry.kind === 'runner_lost'}
              <a href="/runners/{entry.runner_id}">Open the runner</a>
            {/if}
          </p>
        </div>
      </li>
    {/each}
  </ol>
{/if}

<style>
  .timeline {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    position: relative;
    display: flex;
    gap: var(--z-space-3);
    padding-bottom: var(--z-space-3);
  }
  li:last-child {
    padding-bottom: 0;
  }
  /* The thread between the entries. It stops at the last one. */
  li:not(:last-child)::before {
    content: '';
    position: absolute;
    left: 4px;
    top: var(--z-space-4);
    bottom: 0;
    width: 1px;
    background: var(--z-border);
  }
  .marker {
    display: flex;
    align-items: center;
    height: var(--z-leading-sm);
    flex: none;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .head {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-2);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }
  .kind {
    font-weight: var(--z-weight-medium);
    color: var(--entry-colour);
  }
  .source {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .head :global(.when) {
    margin-left: auto;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .message {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .message a {
    color: var(--z-accent);
  }
</style>
