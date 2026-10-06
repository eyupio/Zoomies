<!--
  One thing that needs attention: what is true, why it matters, what to change,
  and a way to get to the thing it is about.
-->
<script lang="ts">
  import { Clock, Layers, Undo2, Wrench, X } from '@lucide/svelte';
  import type { Problem } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import { notifications, SNOOZE_OPTIONS } from '$lib/state/notifications.svelte';
  import type { MenuItem } from '$lib/components/DropdownMenu.svelte';
  import DropdownMenu from '$lib/components/DropdownMenu.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import ApplyRemedy from './ApplyRemedy.svelte';

  interface Props {
    problem: Problem;
    /** Put this one away until the controller stops reporting it. Offered on
     * everything the operator has not read yet, alongside the snooze options. */
    ondismiss?: (problem: Problem) => void;
    /** Put this one away for a fixed while: `ms` is one of `SNOOZE_OPTIONS`. */
    onsnooze?: (problem: Problem, ms: number) => void;
    /** Put every problem of this one's kind away for a fixed while. */
    onsnoozetype?: (problem: Problem, ms: number) => void;
    /** Bring a dismissed or snoozed one back. */
    onrestore?: (problem: Problem) => void;
    /** When it was dismissed, so a decision can be dated before it is undone. */
    dismissedAt?: string;
    /** When a snooze expires and the problem returns on its own, if it was
     * snoozed rather than dismissed outright. */
    snoozedUntil?: string;
    /** The snooze covers every problem of this kind, so restoring this one
     * brings them all back. */
    snoozedByType?: boolean;
    class?: string;
  }

  let {
    problem,
    ondismiss,
    onsnooze,
    onsnoozetype,
    onrestore,
    dismissedAt,
    snoozedUntil,
    snoozedByType = false,
    class: className = '',
  }: Props = $props();

  /** Snooze options first, then dismissing outright -- the menu reads as a
   * choice of how long, with "indefinitely" as the last, most drastic one. */
  const dismissItems = $derived.by<MenuItem[]>(() => [
    ...SNOOZE_OPTIONS.map((option) => ({
      id: `snooze-${option.id}`,
      label: `Snooze for ${option.label}`,
      icon: Clock,
      onSelect: () => onsnooze?.(problem, option.ms),
    })),
    ...(onsnoozetype
      ? SNOOZE_OPTIONS.map((option, i) => ({
          id: `snooze-type-${option.id}`,
          label: `Snooze all like this for ${option.label}`,
          icon: Clock,
          separated: i === 0,
          onSelect: () => onsnoozetype(problem, option.ms),
        }))
      : []),
    {
      id: 'dismiss',
      label: 'Dismiss until resolved',
      icon: X,
      separated: true,
      onSelect: () => ondismiss?.(problem),
    },
  ]);

  /**
   * One line saying which layer set the offending value, or '' when the fix
   * already points at the right place. The file and the defaults say nothing:
   * a value in the file is edited in the file, which is where somebody would
   * have looked anyway.
   */
  const layer = $derived.by(() => {
    switch (problem.source) {
      case 'database':
        return "This value is stored in this fleet's database, so editing the configuration file will not change it. Change it on the Configuration tab, or with the controller stopped:";
      case 'environment':
        return "This value comes from the environment, which is the last word: neither the configuration file nor this fleet's database can change it. With the controller stopped:";
      default:
        return '';
    }
  });

  interface Target {
    href: string;
    label: string;
  }

  /**
   * Where the operator goes next. The pool, the runner, the provider and the
   * machine have pages of their own; the rest land on the list that holds the
   * thing, which is still one click closer than the navigation.
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
        case 'provider':
          return { href: `/providers/${id}`, label: 'Open the provider' };
        case 'ai_context':
          // The card on that page carries the run, the cause and the buttons.
          return { href: '/ai-context', label: 'Open AI Context' };
        case 'machine':
          // A machine has a page of its own because the answer to "why is this
          // taking so long" is on it: the phase timeline, the last failure and
          // the handle that finds the other half of the story at the provider.
          return { href: `/machines/${id}`, label: 'Open the machine' };
        case 'installation':
          return { href: '/installations', label: 'Open installations' };
        case 'job':
          // The list that holds the thing, already narrowed to it: the
          // unmatched job among the unmatched, the lost runner's job among
          // the failed.
          // The fleet's own half, not everything that failed: this problem is
          // about the jobs this deployment broke, and the list it opens should
          // be the one it is talking about.
          if (p.code === 'jobs.runner_lost')
            return { href: '/jobs?faulted=true', label: 'Open failed jobs' };
          if (p.code === 'jobs.unmatched')
            return { href: '/jobs?unmatched=true', label: 'Open unmatched jobs' };
          return { href: '/jobs', label: 'Open jobs' };
        default:
          break;
      }
    }
    // The setting itself, not the page holding eighty-eight of them. A problem
    // that names a key and then lands somebody on a list they have to search is
    // a problem that has done nine tenths of the work and stopped.
    if (p.setting) {
      return {
        href: `/settings/configuration?setting=${encodeURIComponent(p.setting)}`,
        label: 'Open the setting',
      };
    }
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
    {#if problem.remedy}
      <p class="apply"><ApplyRemedy {problem} /></p>
    {/if}
    <!--
      Which layer is setting the value, when that is not the one the fix above
      sends somebody to. An operator told to change a setting will edit the
      configuration file; a value stored here or pinned by the environment is
      one the file cannot change, so they edit it, restart, and get the same
      problem back with nothing learned.
    -->
    {#if layer}
      <p class="layer">
        <Layers size={12} aria-hidden="true" />
        <span>
          {layer}
          {#if problem.undo}
            <code class="undo">{problem.undo}</code>
          {/if}
        </span>
      </p>
    {/if}
    <p class="meta">
      <code>{problem.code}</code>
      {#if problem.setting}<code>{problem.setting}</code>{/if}
      {#if problem.since}<RelativeTime value={problem.since} prefix="since " />{/if}
      {#if dismissedAt}<RelativeTime value={dismissedAt} prefix="dismissed " />{/if}
      {#if snoozedUntil}<RelativeTime value={snoozedUntil} prefix="back " />{/if}
      {#if snoozedByType}<span>snoozed with all like it</span>{/if}
      {#if link}
        <a href={link.href} onclick={() => (notifications.open = false)}>{link.label}</a>
      {/if}
    </p>
  </div>
  {#if ondismiss || onsnooze}
    <span class="action">
      <DropdownMenu
        label="Dismiss or snooze: {problem.title}"
        triggerIcon={X}
        items={dismissItems}
        size="sm"
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
    border-bottom: var(--z-border-width) solid var(--z-border);
    border-left: var(--z-border-width-rail) solid transparent;
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
  .layer {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .layer :global(svg) {
    flex: none;
    margin-top: 0.15em;
  }
  .undo {
    white-space: nowrap;
  }
  .apply {
    margin: var(--z-space-2) 0 0;
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
    border: var(--z-border-width) solid var(--z-border);
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
