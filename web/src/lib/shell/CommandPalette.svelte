<!--
  The command palette.

  It is the fastest path to everything: any page, any pool, any runner by ID or
  name, any host, plus the actions an operator reaches for in a hurry. It reads
  the live fleet cache, so what it finds is what is actually there.
-->
<script lang="ts">
  import {
    Boxes,
    ChartNoAxesCombined,
    CircleSlash,
    GitPullRequestArrow,
    HardDrive,
    LayoutDashboard,
    ListChecks,
    Moon,
    Plug,
    Plus,
    ScrollText,
    Search,
    Server,
    Settings,
    TriangleAlert,
  } from '@lucide/svelte';
  import type { LucideIcon } from '@lucide/svelte';
  import { cordonHost, drainRunner } from '../api/client';
  import { layers, trapFocus } from '../keys';
  import { router } from '../router';
  import { fleet } from '../state/fleet.svelte';
  import { notifications } from '../state/notifications.svelte';
  import { session } from '../state/session.svelte';
  import { theme } from '../state/theme.svelte';
  import { toasts } from '../state/toasts.svelte';
  import { pluralise } from '../format';
  import { runnerStatus } from '../status';
  import EmptyState from '../components/EmptyState.svelte';
  import Logo from '../components/Logo.svelte';

  interface Props {
    open?: boolean;
  }

  let { open = $bindable(false) }: Props = $props();

  interface Command {
    id: string;
    label: string;
    /** "Pool", "Runner", "Go to" -- shown before the label. */
    group: string;
    icon: LucideIcon;
    /** Extra text the search matches on: ids, labels, host names. */
    keywords?: string;
    /** Shown on the right: a state, a pool name. */
    detail?: string;
    run: () => void;
  }

  /** Enough to find anything an operator is looking at, without a 5000-row scan. */
  const ENTITY_LIMIT = 300;

  let query = $state('');
  let active = $state(0);
  let listbox = $state<HTMLUListElement | null>(null);

  const canOperate = $derived(session.can('operator'));

  const commands = $derived.by<Command[]>(() => {
    const out: Command[] = [
      {
        id: 'go-overview',
        group: 'Go to',
        label: 'Overview',
        icon: LayoutDashboard,
        run: () => router.navigate('/'),
      },
      {
        id: 'go-pools',
        group: 'Go to',
        label: 'Pools',
        icon: Boxes,
        run: () => router.navigate('/pools'),
      },
      {
        id: 'go-runners',
        group: 'Go to',
        label: 'Runners',
        icon: Server,
        run: () => router.navigate('/runners'),
      },
      {
        id: 'go-jobs',
        group: 'Go to',
        label: 'Jobs',
        icon: ListChecks,
        run: () => router.navigate('/jobs'),
      },
      {
        id: 'go-usage',
        group: 'Go to',
        label: 'Usage',
        icon: ChartNoAxesCombined,
        run: () => router.navigate('/usage'),
      },
      {
        id: 'go-hosts',
        group: 'Go to',
        label: 'Hosts',
        icon: HardDrive,
        run: () => router.navigate('/hosts'),
      },
      {
        id: 'go-installations',
        group: 'Go to',
        label: 'Installations',
        icon: Plug,
        run: () => router.navigate('/installations'),
      },
      {
        id: 'go-migrate',
        group: 'Go to',
        label: 'Migrate repositories',
        icon: GitPullRequestArrow,
        run: () => router.navigate('/migrate'),
      },
      {
        id: 'go-audit',
        group: 'Go to',
        label: 'Audit',
        icon: ScrollText,
        run: () => router.navigate('/audit'),
      },
      {
        id: 'go-settings',
        group: 'Go to',
        label: 'Settings',
        icon: Settings,
        run: () => router.navigate('/settings'),
      },
      {
        id: 'problems',
        group: 'Action',
        label: 'Show problems',
        detail:
          notifications.active.length === 0
            ? 'nothing needs your attention'
            : pluralise(notifications.active.length, 'problem'),
        icon: TriangleAlert,
        run: () => (notifications.open = true),
      },
      {
        id: 'theme',
        group: 'Action',
        label: 'Switch the theme',
        detail: theme.choice,
        icon: Moon,
        run: () => theme.cycle(),
      },
    ];

    if (canOperate) {
      // The three things a fresh controller needs doing, in the order it needs
      // them. The palette advertises itself as the fastest path to everything,
      // and on a new install the only action it offered was the second step.
      out.push(
        {
          id: 'connect-github',
          group: 'Action',
          label: 'Connect GitHub',
          icon: Plug,
          run: () => router.navigate('/installations'),
        },
        {
          id: 'add-host',
          group: 'Action',
          label: 'Add a host',
          icon: HardDrive,
          run: () => router.navigate('/hosts/new'),
        },
        {
          id: 'create-pool',
          group: 'Action',
          label: 'Create a pool',
          icon: Plus,
          run: () => router.navigate('/pools/new'),
        },
      );
    }

    for (const pool of fleet.pools.slice(0, ENTITY_LIMIT)) {
      if (!pool.id) continue;
      const id = pool.id;
      out.push({
        id: `pool-${id}`,
        group: 'Pool',
        label: pool.name ?? id,
        keywords: `${id} ${(pool.labels ?? []).join(' ')}`,
        detail: pool.enabled === false ? 'disabled' : `${pool.counts?.live ?? 0} live`,
        icon: Boxes,
        run: () => router.navigate(`/pools/${id}`),
      });
    }

    for (const runner of fleet.runners.slice(0, ENTITY_LIMIT)) {
      if (!runner.id) continue;
      const id = runner.id;
      const status = runnerStatus(runner.state);
      out.push({
        id: `runner-${id}`,
        group: 'Runner',
        label: runner.name ?? id,
        keywords: `${id} ${runner.pool_name ?? ''} ${runner.host_name ?? ''}`,
        detail: status.label,
        icon: Server,
        run: () => router.navigate(`/runners/${id}`),
      });
      if (canOperate && (runner.state === 'idle' || runner.state === 'busy')) {
        out.push({
          id: `drain-${id}`,
          group: 'Action',
          label: `Drain ${runner.name ?? id}`,
          keywords: `drain ${id}`,
          detail: 'finishes the current job first',
          icon: CircleSlash,
          run: () =>
            void fleet.optimistic(
              id,
              { state: 'draining' },
              () => drainRunner(id),
              `Could not drain ${runner.name ?? id}`,
            ),
        });
      }
    }

    for (const host of fleet.hosts.slice(0, ENTITY_LIMIT)) {
      if (!host.id) continue;
      const id = host.id;
      out.push({
        id: `host-${id}`,
        group: 'Host',
        label: host.name ?? id,
        keywords: `${id} ${host.address ?? ''}`,
        detail: host.cordoned ? 'cordoned' : `${host.active_runners ?? 0} of ${host.capacity ?? 0}`,
        icon: HardDrive,
        run: () => router.navigate('/hosts'),
      });
      if (canOperate && !host.cordoned) {
        out.push({
          id: `cordon-${id}`,
          group: 'Action',
          label: `Cordon ${host.name ?? id}`,
          keywords: `cordon ${id}`,
          detail: 'keeps its runners, accepts no new ones',
          icon: CircleSlash,
          run: () =>
            void fleet.optimistic(
              id,
              { cordoned: true },
              () => cordonHost(id, { cordoned: true }),
              `Could not cordon ${host.name ?? id}`,
            ),
        });
      }
    }

    return out;
  });

  /**
   * Prefix beats substring beats subsequence, so typing a runner's first
   * characters puts it top even when a hundred rows contain the same fragment.
   */
  function score(command: Command, needle: string): number {
    const haystack = `${command.label} ${command.keywords ?? ''}`.toLowerCase();
    const label = command.label.toLowerCase();
    if (label.startsWith(needle)) return 1000 - label.length;
    const index = haystack.indexOf(needle);
    if (index >= 0) return 500 - index;
    let cursor = 0;
    for (const character of needle) {
      cursor = haystack.indexOf(character, cursor);
      if (cursor < 0) return -1;
      cursor += 1;
    }
    return 100;
  }

  const results = $derived.by(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return commands.slice(0, 12);
    return commands
      .map((command) => ({ command, rank: score(command, needle) }))
      .filter((entry) => entry.rank >= 0)
      .sort((a, b) => b.rank - a.rank)
      .slice(0, 40)
      .map((entry) => entry.command);
  });

  $effect(() => {
    // Reading `results` keeps the highlight inside the list as it narrows.
    if (active >= results.length) active = Math.max(0, results.length - 1);
  });

  $effect(() => {
    if (!open) return;
    query = '';
    active = 0;
    const layer = layers.push('palette', () => (open = false));
    return () => layers.remove(layer);
  });

  function choose(command: Command | undefined): void {
    if (!command) return;
    open = false;
    try {
      command.run();
    } catch (cause) {
      toasts.fromError(cause, 'That command did not run');
    }
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      active = results.length === 0 ? 0 : (active + 1) % results.length;
      scrollActiveIntoView();
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      active = results.length === 0 ? 0 : (active - 1 + results.length) % results.length;
      scrollActiveIntoView();
    } else if (event.key === 'Enter') {
      event.preventDefault();
      choose(results[active]);
    } else if (event.key === 'Home') {
      event.preventDefault();
      active = 0;
      scrollActiveIntoView();
    } else if (event.key === 'End') {
      event.preventDefault();
      active = Math.max(0, results.length - 1);
      scrollActiveIntoView();
    }
  }

  function scrollActiveIntoView(): void {
    queueMicrotask(() => {
      listbox?.querySelector(`[data-index="${active}"]`)?.scrollIntoView({ block: 'nearest' });
    });
  }
</script>

{#if open}
  <div class="backdrop">
    <button
      type="button"
      class="scrim"
      tabindex="-1"
      aria-hidden="true"
      onclick={() => (open = false)}
    ></button>
    <div class="palette" role="dialog" aria-modal="true" aria-label="Command palette" use:trapFocus>
      <div class="search">
        <Search size={15} aria-hidden="true" />
        <input
          bind:value={query}
          data-autofocus
          type="text"
          role="combobox"
          aria-expanded="true"
          aria-controls="palette-results"
          aria-activedescendant={results.length ? `palette-option-${active}` : undefined}
          aria-label="Search pages, pools, runners and hosts"
          placeholder="Search pages, pools, runners, hosts, or type an action"
          autocomplete="off"
          spellcheck="false"
          onkeydown={onKeydown}
        />
      </div>

      {#if results.length > 0}
        <ul bind:this={listbox} id="palette-results" role="listbox" aria-label="Results">
          {#each results as command, index (command.id)}
            <li
              id="palette-option-{index}"
              data-index={index}
              role="option"
              aria-selected={index === active}
              class:active={index === active}
            >
              <button
                type="button"
                tabindex="-1"
                onclick={() => choose(command)}
                onmousemove={() => (active = index)}
              >
                <command.icon size={14} aria-hidden="true" />
                <span class="group">{command.group}</span>
                <span class="label">{command.label}</span>
                {#if command.detail}<span class="detail">{command.detail}</span>{/if}
              </button>
            </li>
          {/each}
        </ul>
      {:else}
        <EmptyState
          icon={Search}
          title="Nothing matches {query}"
          description="Search by a pool name, a runner ID, a host, or the name of a page."
          compact
        />
      {/if}

      <footer>
        <span><kbd>↑</kbd><kbd>↓</kbd> to move</span>
        <span><kbd>Enter</kbd> to run</span>
        <span><kbd>Esc</kbd> to close</span>
        <!-- The palette floats clear of the page, so it is the one surface that
             has to say for itself whose it is. -->
        <span class="brand"><Logo variant="mark" size={16} label="" /> Zoomies</span>
      </footer>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: var(--z-layer-palette);
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding: 12vh var(--z-space-4) var(--z-space-4);
  }
  .scrim {
    position: absolute;
    inset: 0;
    border: 0;
    padding: 0;
    /* A veil made from the page's own ground, so it dims in light and in dark
       without a hard-coded black that only works in one of them. */
    background: color-mix(in srgb, var(--z-bg) 72%, transparent);
    backdrop-filter: blur(3px);
    cursor: default;
  }
  .palette {
    position: relative;
    display: flex;
    flex-direction: column;
    width: 100%;
    max-width: 620px;
    max-height: 70vh;
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-lg);
    overflow: hidden;
    animation: rise var(--z-motion-base) var(--z-ease);
  }
  .palette:focus {
    outline: none;
  }
  .search {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border-bottom: 1px solid var(--z-border);
    color: var(--z-text-subtle);
  }
  input {
    flex: 1;
    border: 0;
    background: transparent;
    color: var(--z-text);
    font-family: inherit;
    font-size: var(--z-text-lg);
  }
  input:focus {
    outline: none;
  }
  input::placeholder {
    color: var(--z-text-subtle);
  }
  ul {
    margin: 0;
    padding: var(--z-space-1);
    list-style: none;
    overflow-y: auto;
  }
  li button {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    width: 100%;
    padding: var(--z-space-2) var(--z-space-3);
    border: 0;
    border-radius: var(--z-radius-sm);
    background: transparent;
    color: var(--z-text);
    font-family: inherit;
    font-size: var(--z-text-sm);
    text-align: left;
    cursor: pointer;
  }
  li.active button {
    background: var(--z-accent-subtle);
  }
  .group {
    flex: none;
    min-width: 62px;
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-subtle);
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail {
    flex: none;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  footer {
    display: flex;
    align-items: center;
    gap: var(--z-space-4);
    padding: var(--z-space-2) var(--z-space-4);
    border-top: 1px solid var(--z-border);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
  }
  .brand {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    margin-left: auto;
    font-weight: var(--z-weight-semibold);
    color: var(--z-text-muted);
  }
  @media (max-width: 560px) {
    /* The three key hints are the working part of this bar; the signature is
       the first thing to go when the row stops fitting. */
    .brand {
      display: none;
    }
  }
  kbd {
    margin-right: 2px;
    padding: 0 var(--z-space-1);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    font-family: var(--z-font-mono);
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(-8px);
    }
  }
</style>
