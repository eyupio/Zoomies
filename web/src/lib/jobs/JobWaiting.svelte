<!--
  What the fleet is doing about a job that is still queued.

  A queued job's row says how long it has waited; this says why. The pool that
  claimed it is read from the live fleet cache, so the counts move as runners
  come up, and the scheduler's own reason for not placing more -- no host with
  room, the pool at its ceiling -- is printed in its words rather than
  paraphrased.
-->
<script lang="ts">
  import type { Job } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { pluralise } from '$lib/format';
  import Duration from '$lib/components/Duration.svelte';

  interface Props {
    job: Job;
    class?: string;
  }

  let { job, class: className = '' }: Props = $props();

  const pool = $derived(fleet.pool(job.pool_id));
  const counts = $derived(pool?.counts);
  const warming = $derived((counts?.provisioning ?? 0) + (counts?.registering ?? 0));
  const idle = $derived(counts?.idle ?? 0);
  const busy = $derived(counts?.busy ?? 0);
  const atCeiling = $derived(
    pool !== undefined && (counts?.live ?? 0) >= (pool.max_runners ?? Number.POSITIVE_INFINITY),
  );
  const blocked = $derived((pool?.warnings ?? []).filter((w) => w.code === 'pool.no_capacity'));

  const summary = $derived.by(() => {
    if (!pool) return 'The pool that claimed it is not in view yet.';
    if (warming > 0) {
      return `${pluralise(warming, 'runner is', 'runners are')} starting for this pool; the job goes to the first one GitHub sees.`;
    }
    if (idle > 0) {
      return `${pluralise(idle, 'runner is', 'runners are')} idle in this pool, so GitHub should hand it over any moment.`;
    }
    if (atCeiling) {
      return `The pool is at its ceiling of ${pluralise(pool.max_runners ?? 0, 'runner')}, all busy. The job waits for one to finish.`;
    }
    if (blocked.length > 0) return 'The scheduler wants a runner for it and cannot place one.';
    return 'No runner is free and none is starting yet. The scheduler decides on its next pass.';
  });
</script>

<div class="waiting {className}" role="status" aria-label="What is happening to this job">
  <p class="line">
    <span class="lead">Waiting</span>
    <span class="tabular"><Duration from={job.queued_at} live /></span>
    {#if pool}
      <span>in <a href="/pools/{pool.id}">{pool.name ?? pool.id}</a></span>
    {/if}
  </p>
  <p class="summary">{summary}</p>
  {#if counts}
    <dl class="counts">
      <div>
        <dt>Starting</dt>
        <dd class="tabular">{warming}</dd>
      </div>
      <div>
        <dt>Idle</dt>
        <dd class="tabular">{idle}</dd>
      </div>
      <div>
        <dt>Busy</dt>
        <dd class="tabular">{busy}</dd>
      </div>
      <div>
        <dt>Ceiling</dt>
        <dd class="tabular">{pool?.max_runners ?? '--'}</dd>
      </div>
    </dl>
  {/if}
  {#each blocked as problem (problem.title)}
    <p class="blocked">{problem.title}{problem.fix ? ` ${problem.fix}` : ''}</p>
  {/each}
</div>

<style>
  .waiting {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-3);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    color: var(--z-text);
  }
  .lead {
    font-weight: var(--z-weight-semibold);
  }
  .line a {
    color: var(--z-accent);
  }
  .summary,
  .blocked {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    max-width: 70ch;
  }
  .blocked {
    color: var(--z-danger);
  }
  .counts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2) var(--z-space-5);
    margin: 0;
  }
  dt {
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-subtle);
  }
  dd {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
</style>
