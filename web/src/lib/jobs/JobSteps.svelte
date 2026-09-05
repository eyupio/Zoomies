<!--
  The job's steps, as GitHub last reported them.

  On a finished job every step carries its conclusion, and the one that failed
  is set apart so it can be found without reading the list. On a running job
  the current step is the one still counting; the ones after it have not
  started. GitHub sends the list on each delivery rather than per step, so this
  is as live as the webhooks are, and no livelier.
-->
<script lang="ts">
  import type { JobStep } from '$lib/api/types';
  import { toMillis } from '$lib/format';
  import { stepStatus } from '$lib/status';
  import Duration from '$lib/components/Duration.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';

  interface Props {
    steps: readonly JobStep[];
    /** The step the job stopped at, when it did. Drawn set apart. */
    failedStep?: JobStep | null;
    /** True while the job runs, so the current step keeps counting. */
    running?: boolean;
    class?: string;
  }

  let { steps, failedStep = null, running = false, class: className = '' }: Props = $props();

  function took(step: JobStep): number | null {
    const from = toMillis(step.started_at);
    const to = toMillis(step.completed_at);
    return from === null || to === null ? null : Math.max(0, to - from);
  }

  function isFailed(step: JobStep): boolean {
    return (
      failedStep !== null && failedStep.number === step.number && failedStep.name === step.name
    );
  }
</script>

{#if steps.length === 0}
  <EmptyState
    compact
    title="No steps reported yet"
    description="GitHub lists a job's steps once it has been handed to a runner."
  />
{:else}
  <ol class="steps {className}" aria-label="Steps">
    {#each steps as step (`${step.number}-${step.name}`)}
      {@const status = stepStatus(step)}
      {@const live = running && step.status === 'in_progress'}
      <li class:failed={isFailed(step)} class:live>
        <span class="number tabular">{step.number}</span>
        <StatusDot {status} size="sm" />
        <span class="name">{step.name ?? 'Unnamed step'}</span>
        <span class="took tabular">
          {#if live && step.started_at}
            <Duration from={step.started_at} live />
            <span class="still">so far</span>
          {:else if took(step) !== null}
            <Duration ms={took(step)} />
          {:else}
            <span class="none">{status.label}</span>
          {/if}
        </span>
      </li>
    {/each}
  </ol>
{/if}

<style>
  .steps {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: grid;
    grid-template-columns: 1.5rem auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--z-space-2);
    padding: var(--z-space-1) var(--z-space-2);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  li.failed {
    background: var(--z-danger-subtle);
    font-weight: var(--z-weight-semibold);
  }
  li.live .name {
    color: var(--z-busy);
  }
  .number {
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
    text-align: end;
  }
  .name {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .took {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    white-space: nowrap;
  }
  .still,
  .none {
    color: var(--z-text-subtle);
  }
</style>
