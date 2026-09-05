<!--
  One thing that needs attention: what is true, why it matters, what to change,
  and a way to get to the thing it is about.
-->
<script lang="ts">
  import { Undo2, Wrench, X } from '@lucide/svelte';
  import type { Problem } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import IconButton from '$lib/components/IconButton.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';

  interface Props {
    problem: Problem;
    /** Put this one away. Offered on everything the operator has not read yet. */
    ondismiss?: (problem: Problem) => void;
    /** Bring a dismissed one back. */
    onrestore?: (problem: Problem) => void;
    /** When it was dismissed, so a decision can be dated before it is undone. */
    dismissedAt?: string;
    class?: string;
  }

  let { problem, ondismiss, onrestore, dismissedAt, class: className = '' }: Props = $props();

  interface Target {
    href: string;
    label: string;
  }

  /**
   * Where the operator goes next. Only the pool and the runner have pages of
   * their own; the rest land on the list that holds the thing, which is still
   * one click closer than the navigation.
   */
  function target(p: Problem): Target | null {
    const id = p.target_id;
    if (id) {
      switch (p.target_kind) {
        case 'pool':
          return { href: `/pools/${id}`, label: 'Open the pool' };
        case 'runner':
          return { href: `/runners/${id}`, label: 'Open the runner' };
        case 'host':
          return { href: '/hosts', label: 'Open hosts' };
        case 'installation':
          return { href: '/installations', label: 'Open installations' };
        case 'job':
          // The list that holds the thing, already narrowed to it: the
          // unmatched job among the unmatched, the lost runner's job among
          // the failed.
          if (p.code === 'jobs.runner_lost')
            return { href: '/jobs?failed=true', label: 'Open failed jobs' };
          if (p.code === 'jobs.unmatched')
            return { href: '/jobs?unmatched=true', label: 'Open unmatched jobs' };
          return { href: '/jobs', label: 'Open jobs' };
        default:
          break;
      }
    }
    if (p.setting) return { href: '/settings', label: 'Open settings' };
    return null;
  }

  const status = $derived(severityStatus(problem.severity));
  const link = $derived(target(problem));
</script>

<li class="problem {className}" class:dimmed={!!onrestore} data-severity={status.key}>
  <span class="shape"><StatusDot {status} /></span>
  <div class="body">
    <p class="title">{problem.title}</p>
    {#if problem.detail}
      <p class="detail"><RemedyText text={problem.detail} /></p>
    {/if}
    {#if problem.fix}
      <p class="fix">
        <Wrench size={12} aria-hidden="true" />
        <span><RemedyText text={problem.fix} /></span>
      </p>
    {/if}
    <p class="meta">
      <code>{problem.code}</code>
      {#if problem.setting}<code>{problem.setting}</code>{/if}
      {#if problem.since}<RelativeTime value={problem.since} prefix="since " />{/if}
      {#if dismissedAt}<RelativeTime value={dismissedAt} prefix="dismissed " />{/if}
      {#if link}<a href={link.href}>{link.label}</a>{/if}
    </p>
  </div>
  {#if ondismiss}
    <span class="action">
      <IconButton
        icon={X}
        label="Dismiss: {problem.title}"
        size="sm"
        onclick={() => ondismiss(problem)}
      />
    </span>
  {:else if onrestore}
    <span class="action">
      <IconButton
        icon={Undo2}
        label="Restore: {problem.title}"
        size="sm"
        onclick={() => onrestore(problem)}
      />
    </span>
  {/if}
</li>

<style>
  .problem {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
    border-left: 3px solid transparent;
  }
  .problem:last-child {
    border-bottom: 0;
  }
  .problem[data-severity='error'] {
    border-left-color: var(--z-danger);
    background: var(--z-danger-subtle);
  }
  .problem[data-severity='warning'] {
    border-left-color: var(--z-pending);
  }
  /* Something already read is still legible, but it has stopped asking. */
  .problem.dimmed {
    background: none;
    opacity: 0.72;
  }
  .shape {
    display: inline-flex;
    align-items: center;
    height: var(--z-leading-base);
    flex: none;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  /* The dismiss control is the operator's, not the fault's: it sits out of the
     reading order of the four sentences and never competes with the fix. */
  .action {
    flex: none;
    margin-top: calc(var(--z-space-1) * -1);
  }
  .title {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .detail {
    margin: var(--z-space-1) 0 0;
    max-width: 78ch;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .fix {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: var(--z-space-2) 0 0;
    max-width: 78ch;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .fix :global(svg) {
    flex: none;
    margin-top: var(--z-space-1);
    color: var(--z-text-subtle);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-1) var(--z-space-3);
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .meta code {
    padding: 0 var(--z-space-1);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
  }
  .meta a {
    color: var(--z-accent);
    font-weight: var(--z-weight-medium);
    text-decoration: none;
  }
  .meta a:hover {
    text-decoration: underline;
  }
</style>
