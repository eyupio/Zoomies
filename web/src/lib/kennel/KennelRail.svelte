<!--
  The Kennel Club section's own navigation: a rail at the left where there is
  room, a strip above the page where there is not. It is the shape Settings
  uses, for the same reason: the map is always in view, and another page is one
  more row and not one more button squeezed into a header that was already full.

  AI Context has a row of its own because it is a feature of its own, and it used
  to be one button among several in a page header, which is why nobody found it.
  It sits apart from the group the switch governs, because it keeps working with
  Kennel Club off.
-->
<script lang="ts">
  import { BookOpenText, LayoutDashboard, ListChecks } from '@lucide/svelte';
  import type { LucideIcon } from '@lucide/svelte';
  import KennelSwitch from './KennelSwitch.svelte';
  import { KENNEL_GROUPS, type KennelPageId } from './pages';

  // Drawn here and not in the registry, so the registry stays plain data a unit
  // test can read without a component compiler.
  const ICONS: Record<KennelPageId, LucideIcon> = {
    overview: LayoutDashboard,
    repositories: ListChecks,
    'ai-context': BookOpenText,
  };

  interface Props {
    /** The page on screen. */
    current: KennelPageId;
  }

  let { current }: Props = $props();

  const uid = $props.id();
</script>

<nav class="rail" aria-label="Kennel Club">
  {#each KENNEL_GROUPS as group (group.id)}
    <div class="group">
      <p class="group-label" id="kennel-group-{uid}-{group.id}">{group.label}</p>
      {#if group.switch}<KennelSwitch class="rail-switch" />{/if}
      <ul aria-labelledby="kennel-group-{uid}-{group.id}">
        {#each group.pages as page (page.id)}
          {@const here = page.id === current}
          {@const Icon = ICONS[page.id]}
          <li>
            <a href={page.path} aria-current={here ? 'page' : undefined} class:current={here}>
              <Icon size={16} aria-hidden="true" />
              <span class="label">{page.label}</span>
            </a>
          </li>
        {/each}
      </ul>
    </div>
  {/each}
</nav>

<style>
  /* The same width as Settings' rail, so the two sections sit alike. */
  .rail {
    position: sticky;
    top: calc(var(--z-topbar-height) + var(--z-space-6));
    flex: none;
    width: var(--z-settings-rail-width);
  }
  .group + .group {
    margin-top: var(--z-space-4);
  }
  .group-label {
    margin: 0 0 var(--z-space-1);
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    font-weight: var(--z-weight-medium);
    letter-spacing: var(--z-tracking-wide);
    text-transform: uppercase;
    color: var(--z-text-subtle);
  }
  ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  a {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    height: var(--z-space-8);
    padding: 0 var(--z-space-2);
    border-radius: var(--z-radius-md);
    color: var(--z-text-muted);
    text-decoration: none;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    white-space: nowrap;
    transition:
      background-color var(--z-motion-fast) var(--z-ease),
      color var(--z-motion-fast) var(--z-ease);
  }
  a:hover {
    background: var(--z-surface-hover);
    color: var(--z-text);
  }
  a.current {
    background: var(--z-accent-subtle);
    color: var(--z-accent);
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  /* A finger needs the height the rest of the UI gives it, in the rail and the strip alike. */
  @media (pointer: coarse) {
    a {
      height: var(--z-control-touch);
    }
  }
  /*
    Narrower than a rail can have beside a table, the page's links run across the
    top instead, as Settings' do, but wrapped and not scrolled: the switch takes a
    line of its own and the pages sit under it, so none of them -- AI Context least
    of all -- can be pushed out of sight to the right on a phone. The group names
    go; three chips and a switch are their own map.
  */
  @media (max-width: 1180px) {
    .rail {
      position: static;
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--z-space-2) var(--z-space-3);
      width: auto;
      max-width: 100%;
    }
    /* The groups dissolve, so the switch and each list are items of the one row. */
    .group {
      display: contents;
    }
    .group + .group {
      margin-top: 0;
    }
    .group-label {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
      white-space: nowrap;
    }
    .rail :global(.rail-switch) {
      flex: 1 0 100%;
      padding: 0;
    }
    ul {
      flex-direction: row;
      flex-wrap: wrap;
    }
    a {
      padding: 0 var(--z-space-3);
      border: var(--z-border-width) solid var(--z-border);
      background: var(--z-surface);
    }
    a.current {
      border-color: var(--z-accent-border);
    }
  }
</style>
