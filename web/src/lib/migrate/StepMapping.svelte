<!--
  Step three: what each GitHub label becomes.

  This is the whole decision. `runs-on: ubuntu-latest` is a promise about the
  machine a job gets, and moving it to a pool is a promise that the pool keeps
  it. Zoomies proposes a pool for each label by matching the operating system
  and architecture the hosted label implies, and proposes nothing at all where
  no pool plausibly matches -- an unmapped label leaves those jobs on GitHub's
  runners, which is a smaller surprise than a job that queues forever.

  "Leave it alone" is always an option, and it is the default for anything the
  server could not place.
-->
<script lang="ts">
  import type { MigrationPlan } from '$lib/api/types';
  import Select from '$lib/components/Select.svelte';

  interface Props {
    plan: MigrationPlan | null;
    mapping: Record<string, string>;
    onchange: (label: string, to: string) => void;
  }

  let { plan, mapping, onchange }: Props = $props();

  const LEAVE = '';

  const options = $derived([
    { value: LEAVE, label: 'Leave it alone — keep running on GitHub' },
    ...(plan?.pools ?? []).map((p) => ({
      value: p.runs_on ?? '',
      label: `${p.runs_on} — the ${p.name} pool`,
    })),
  ]);

  const labels = $derived(plan?.hosted_labels ?? []);
  const mapped = $derived(labels.filter((l) => (mapping[l] ?? '') !== '').length);
</script>

{#if labels.length === 0}
  <p class="lede">
    None of the repositories you picked asks for a GitHub-hosted runner, so there is nothing to map.
  </p>
{:else}
  <p class="lede">
    {mapped} of {labels.length}
    {labels.length === 1 ? 'label is' : 'labels are'} pointed at a pool. Anything left alone keeps running
    on GitHub's runners, and the review step will say so job by job.
  </p>

  <ul class="mapping">
    {#each labels as label (label)}
      {@const to = mapping[label] ?? ''}
      <li>
        <code class="from">{label}</code>
        <span class="arrow" aria-hidden="true">→</span>
        <Select
          value={to}
          {options}
          size="sm"
          ariaLabel="What replaces {label}"
          onchange={(value) => onchange(label, value)}
          class="to"
        />
      </li>
    {/each}
  </ul>

  {#if (plan?.pools ?? []).length === 0}
    <p class="warn">
      This installation has no enabled pool, so there is nothing to point a label at. Create one on
      the Pools page first.
    </p>
  {/if}
{/if}

<style>
  .lede {
    margin: 0 0 var(--z-space-4);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .mapping {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .mapping li {
    display: grid;
    grid-template-columns: minmax(0, 14rem) auto minmax(0, 1fr);
    align-items: center;
    gap: var(--z-space-3);
  }
  .from {
    padding: var(--z-space-1) var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .arrow {
    color: var(--z-text-subtle);
  }
  .warn {
    margin: var(--z-space-4) 0 0;
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    color: var(--z-pending);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
</style>
