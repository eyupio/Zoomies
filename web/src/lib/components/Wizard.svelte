<!--
  A multi-step form. Used by pool creation: target, labels, backend, scaling,
  review. The step list is a real list with `aria-current`, so where you are is
  never only a colour.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Check } from '@lucide/svelte';
  import Button from './Button.svelte';

  export interface WizardStep {
    id: string;
    title: string;
    description?: string;
  }

  interface Props {
    steps: readonly WizardStep[];
    current?: number;
    /** False while the current step is invalid; the Next button waits. */
    canAdvance?: boolean;
    busy?: boolean;
    nextLabel?: string;
    backLabel?: string;
    finishLabel?: string;
    cancelLabel?: string;
    onnext?: () => void | Promise<void>;
    onback?: () => void;
    onfinish?: () => void | Promise<void>;
    oncancel?: () => void;
    class?: string;
    children: Snippet<[WizardStep, number]>;
  }

  let {
    steps,
    current = $bindable(0),
    canAdvance = true,
    busy = false,
    nextLabel = 'Next',
    backLabel = 'Back',
    finishLabel = 'Create',
    cancelLabel = 'Cancel',
    onnext,
    onback,
    onfinish,
    oncancel,
    class: className = '',
    children,
  }: Props = $props();

  const step = $derived(steps[current] ?? steps[0]);
  const isLast = $derived(current >= steps.length - 1);

  async function next(): Promise<void> {
    if (!canAdvance || busy) return;
    if (isLast) {
      await onfinish?.();
      return;
    }
    await onnext?.();
    if (current < steps.length - 1) current += 1;
  }

  function back(): void {
    if (current === 0) {
      oncancel?.();
      return;
    }
    onback?.();
    current -= 1;
  }
</script>

<div class="wizard {className}">
  <ol class="steps">
    {#each steps as s, i (s.id)}
      <li class:done={i < current} class:active={i === current}>
        <span class="marker" aria-hidden="true">
          {#if i < current}<Check size={12} />{:else}{i + 1}{/if}
        </span>
        <span class="step-text">
          <span class="step-title" aria-current={i === current ? 'step' : undefined}>{s.title}</span
          >
          {#if s.description}<span class="step-description">{s.description}</span>{/if}
        </span>
      </li>
    {/each}
  </ol>

  <div class="panel">
    {#if step}
      <h2>{step.title}</h2>
      {#if step.description}<p class="lede">{step.description}</p>{/if}
      <div class="content">{@render children(step, current)}</div>
    {/if}
  </div>

  <footer>
    <Button variant="ghost" onclick={back} disabled={busy}>
      {current === 0 ? cancelLabel : backLabel}
    </Button>
    <p class="progress" aria-live="polite">Step {current + 1} of {steps.length}</p>
    <Button variant="primary" onclick={next} disabled={!canAdvance} loading={busy}>
      {isLast ? finishLabel : nextLabel}
    </Button>
  </footer>
</div>

<style>
  .wizard {
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr);
    grid-template-areas: 'steps panel' 'steps footer';
    gap: var(--z-space-6);
    align-items: start;
  }
  .steps {
    grid-area: steps;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .steps li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
  }
  .marker {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-5);
    height: var(--z-space-5);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-full);
    background: var(--z-surface);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .active .marker {
    border-color: var(--z-accent);
    background: var(--z-accent);
    color: var(--z-accent-contrast);
  }
  .done .marker {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
    color: var(--z-idle);
  }
  .step-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding-top: 2px;
  }
  .step-title {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .active .step-title {
    color: var(--z-text);
  }
  .step-description {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .panel {
    grid-area: panel;
    padding: var(--z-space-6);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    font-weight: var(--z-weight-semibold);
  }
  .lede {
    margin: var(--z-space-1) 0 0;
    max-width: 68ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .content {
    margin-top: var(--z-space-5);
  }
  footer {
    grid-area: footer;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-4);
  }
  .progress {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  @media (max-width: 1180px) {
    .wizard {
      grid-template-columns: minmax(0, 1fr);
      grid-template-areas: 'steps' 'panel' 'footer';
    }
    .steps {
      flex-direction: row;
      flex-wrap: wrap;
      gap: var(--z-space-4);
    }
    .step-description {
      display: none;
    }
  }
</style>
