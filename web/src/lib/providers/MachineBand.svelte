<!--
  The machines a provider is renting, above the hosts they become.

  It is on the Hosts page rather than behind Providers because the question it
  answers is asked here: an operator looking at a fleet with too few hosts wants
  to know whether more are on their way. The band is the same ordered flow the
  runner lifecycle is drawn as, for the same reason -- the store enforces the
  order, so this is the fleet's own shape rather than a diagram somebody drew.

  The switch stops new machines being bought. It is deliberately the only
  control here: it survives a restart because it is a column on the provider
  row, and it blocks nothing else -- a drain finishes, a delete completes, and a
  machine already being built is still followed to wherever it ends up. An
  operator who presses it during an incident has stopped the spending without
  stranding a VM.
-->
<script lang="ts">
  import { TriangleAlert } from '@lucide/svelte';
  import type { Machine, Provider } from '$lib/api/types';
  import { formatNumber, pluralise } from '$lib/format';
  import { machineStatus } from '$lib/status';
  import { lifecycleCounts, machinesNeedingReview } from './machines';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';

  interface Props {
    machines: readonly Machine[];
    providers: readonly Provider[];
    canOperate?: boolean;
    /** Press the kill switch, or let it up again, across every provider. */
    onpause: (paused: boolean) => void;
    class?: string;
  }

  let { machines, providers, canOperate = false, onpause, class: className = '' }: Props = $props();

  const steps = $derived(lifecycleCounts(machines));
  const live = $derived(steps.reduce((n, step) => n + step.count, 0));
  const review = $derived(machinesNeedingReview(machines));
  // Paused when every provider is: a fleet with one provider paused and one
  // running is still buying machines, and a switch that read as "on" would be
  // a lie about exactly the thing somebody pressed it to stop.
  const paused = $derived(providers.length > 0 && providers.every((p) => p.paused === true));
  const partly = $derived(!paused && providers.some((p) => p.paused === true));
</script>

<section class="band {className}" aria-labelledby="machines-band-heading">
  <header>
    <div>
      <h2 id="machines-band-heading">Machines</h2>
      <p>
        {pluralise(live, 'machine')} rented from {pluralise(providers.length, 'provider')}. Each one
        becomes a host when its agent joins.
      </p>
    </div>
    <Switch
      checked={paused}
      disabled={!canOperate}
      label="Pause new machines"
      description={paused
        ? 'Nothing new is being bought. Drains, deletes and machines already on their way carry on.'
        : partly
          ? 'One provider is already paused. Switching this on pauses the rest.'
          : 'Stops anything new being bought. Drains, deletes and machines already on their way carry on.'}
      onchange={(next) => onpause(next)}
    />
  </header>

  <ol class="steps" aria-label="Machine lifecycle, in order">
    {#each steps as step, i (step.state)}
      {@const status = machineStatus(step.state)}
      <li class="step" data-tone={status.tone} class:empty={step.count === 0}>
        <Tooltip text="{status.label}: {pluralise(step.count, 'machine')}. {status.hint ?? ''}">
          {#snippet content()}
            <strong>{status.label}</strong>
            <span class="hint">{status.hint}</span>
          {/snippet}
          <a href="/providers?state={step.state}" aria-label="{status.label} machines">
            <span class="head"><StatusDot {status} size="sm" />{status.label}</span>
            <span class="count">{formatNumber(step.count)}</span>
          </a>
        </Tooltip>
        <!-- Draining goes back to ready when work returns, which is what stops
             a quiet ten minutes costing the price of a new machine. Every other
             step is one way. The arrow past the last one holds its column so
             each card is the same width however many the flow wraps to. -->
        <span class="arrow" aria-hidden="true" class:spent={i === steps.length - 1}
          >{step.state === 'ready' ? '⇄' : '→'}</span
        >
      </li>
    {/each}
  </ol>

  {#if review.length > 0}
    <p class="review">
      <TriangleAlert size={14} aria-hidden="true" />
      <span>
        {pluralise(review.length, 'machine')}
        {review.length === 1 ? 'needs' : 'need'} review. Nothing here will delete
        {review.length === 1 ? 'it' : 'them'} on its own, a resource we cannot prove is ours is left exactly
        where it is.
        <a href="/providers">Open providers</a>
      </span>
    </p>
  {/if}
</section>

<style>
  .band {
    padding: var(--z-space-4) var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    flex-wrap: wrap;
    margin-bottom: var(--z-space-4);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  /* A grid rather than a wrapping row, for LifecycleFlow's reason: wrapped
     flex items are as wide as their own label, so "Bootstrapping" and "Ready"
     end up different sizes and nothing lines up underneath them. */
  .steps {
    display: grid;
    grid-template-columns: repeat(7, minmax(0, 1fr));
    align-items: stretch;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .step {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: stretch;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .step :global(.tip-wrap) {
    display: flex;
    min-width: 0;
  }
  .step a {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    height: 100%;
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-left: var(--z-border-width-rail) solid var(--tone);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    color: var(--z-text);
    text-decoration: none;
    transition:
      border-color var(--z-motion-fast) var(--z-ease),
      background-color var(--z-motion-fast) var(--z-ease);
  }
  .step a:hover {
    border-color: var(--z-border-strong);
    border-left-color: var(--tone);
    background: var(--z-surface-hover);
  }
  .step.empty a {
    background: var(--z-surface-sunken);
  }
  .step.empty .count {
    color: var(--z-text-subtle);
  }
  [data-tone='busy'] {
    --tone: var(--z-busy);
  }
  [data-tone='idle'] {
    --tone: var(--z-idle);
  }
  [data-tone='pending'] {
    --tone: var(--z-pending);
  }
  [data-tone='draining'] {
    --tone: var(--z-draining);
  }
  [data-tone='danger'] {
    --tone: var(--z-danger);
  }
  [data-tone='neutral'] {
    --tone: var(--z-neutral);
  }
  .head {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--z-space-2);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .count {
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    font-variant-numeric: tabular-nums;
  }
  .arrow {
    align-self: center;
    width: var(--z-space-3);
    text-align: center;
    color: var(--z-text-subtle);
    font-size: var(--z-text-sm);
  }
  .arrow.spent {
    visibility: hidden;
  }
  .hint {
    display: block;
    color: var(--z-text-muted);
  }
  .review {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: var(--z-space-4) 0 0;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-danger-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .review :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-2);
    color: var(--z-danger);
  }
  @media (max-width: 1180px) {
    .steps {
      grid-template-columns: repeat(4, minmax(0, 1fr));
    }
    .step:nth-child(4n) .arrow {
      visibility: hidden;
    }
  }
  @media (max-width: 768px) {
    .steps {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .step:nth-child(4n) .arrow {
      visibility: visible;
    }
    .step:nth-child(2n) .arrow {
      visibility: hidden;
    }
    .step {
      gap: var(--z-space-1);
    }
    .arrow {
      width: var(--z-space-2);
    }
  }
</style>
