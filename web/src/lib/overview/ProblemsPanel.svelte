<!--
  The problems panel.

  When something is wrong this is the loudest thing on the page: errors first,
  then warnings, each with the detail and the fix the controller wrote.

  When nothing is wrong it is one quiet line. Not an empty box, not a green
  banner, not a card with a tick in it. An operator who glances at this page
  fifty times a day should be told "nothing" in the smallest way that can still
  be read from across the room.
-->
<script lang="ts">
  import type { Problem, Severity } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { joinWords, pluralise } from '$lib/format';
  import { severityStatus } from '$lib/status';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import Panel from './Panel.svelte';
  import ProblemItem from './ProblemItem.svelte';

  interface Props {
    loading?: boolean;
    class?: string;
    /**
     * True while the first-run checklist above is on screen. The all-clear line
     * is then suppressed: two panels competing to explain the same emptiness is
     * worse than one, and only one of them is right.
     */
    setupPending?: boolean;
  }

  let { loading = false, setupPending = false, class: className = '' }: Props = $props();

  const ORDER: readonly Severity[] = ['error', 'warning', 'info'];
  const NOUN: Record<Severity, string> = {
    error: 'error',
    warning: 'warning',
    info: 'note',
  };

  /** True while the first-run checklist is on screen saying what is missing. */
  const problems = $derived(fleet.problems);
  const clear = $derived(problems.length === 0 && fleet.problemsOk);

  interface Group {
    severity: Severity;
    items: readonly Problem[];
  }

  const groups = $derived.by(() => {
    const out: Group[] = [];
    for (const severity of ORDER) {
      const items = problems.filter((p) => (p.severity ?? 'info') === severity);
      if (items.length > 0) out.push({ severity, items });
    }
    return out;
  });

  /**
   * One sentence, announced once whenever it changes. The visible copy is
   * hidden from assistive technology so the same words are not read twice.
   */
  const announcement = $derived.by(() => {
    if (loading) return '';
    if (clear) return 'Nothing needs your attention.';
    const parts = groups.map((g) => pluralise(g.items.length, NOUN[g.severity]));
    const verb = problems.length === 1 ? 'needs' : 'need';
    return `${pluralise(problems.length, 'problem')} ${verb} your attention: ${joinWords(parts)}.`;
  });
</script>

<div class="problems {className}">
  <p class="sr-only" aria-live="polite">{announcement}</p>

  {#if loading}
    <div class="loading">
      <Skeleton width="12rem" height="var(--z-text-base)" />
    </div>
  {:else if clear}
    <!--
      "Nothing needs your attention" is true of a working fleet and false of a
      controller that cannot register a runner. The problems endpoint stays
      deliberately quiet on an unconfigured instance -- an instance with no
      installation is not yet misconfigured, it is unfinished -- so the line has
      to stand down while the setup checklist above is the message.
    -->
    {#if !setupPending}
      <p class="quiet" aria-hidden="true">Nothing needs your attention.</p>
    {/if}
  {:else}
    <Panel title="Problems" description="Worst first, with the fix the controller suggests." flush>
      {#each groups as group (group.severity)}
        {@const status = severityStatus(group.severity)}
        <h3 class="group" data-severity={group.severity}>
          <StatusDot {status} size="sm" />
          {pluralise(group.items.length, NOUN[group.severity])}
        </h3>
        <ul class="items">
          {#each group.items as problem, index (`${problem.code}:${problem.target_id ?? ''}:${index}`)}
            <ProblemItem {problem} />
          {/each}
        </ul>
      {/each}
    </Panel>
  {/if}
</div>

<style>
  .problems {
    min-width: 0;
  }
  .loading {
    padding: var(--z-space-1) 0;
  }
  .quiet {
    margin: 0;
    padding: var(--z-space-1) 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .group {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    padding: var(--z-space-2) var(--z-space-5);
    border-top: 1px solid var(--z-border);
    border-bottom: 1px solid var(--z-border);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text-muted);
  }
  .group:first-child {
    border-top: 0;
  }
  .items {
    margin: 0;
    padding: 0;
    list-style: none;
  }
</style>
