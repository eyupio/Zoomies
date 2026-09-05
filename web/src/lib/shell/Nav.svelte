<!--
  The persistent left navigation.

  The order is fixed and matches docs/ui-guidelines.md, because muscle memory is
  the whole point of a persistent nav. It collapses to icons and remembers that
  it did; the `g` shortcut letter is shown beside each entry so the keyboard
  route is discoverable rather than folklore.
-->
<script lang="ts">
  import {
    Boxes,
    GitPullRequestArrow,
    HardDrive,
    LayoutDashboard,
    ListChecks,
    PanelLeftClose,
    PanelLeftOpen,
    Plug,
    ScrollText,
    Server,
    Settings,
  } from '@lucide/svelte';
  import type { LucideIcon } from '@lucide/svelte';
  import { router } from '../router';
  import { prefs } from '../state/prefs.svelte';
  import IconButton from '../components/IconButton.svelte';
  import Logo from '../components/Logo.svelte';

  interface NavItem {
    path: string;
    label: string;
    icon: LucideIcon;
    /** The second key of the `g` chord. */
    key: string;
  }

  const items: readonly NavItem[] = [
    { path: '/', label: 'Overview', icon: LayoutDashboard, key: 'o' },
    { path: '/pools', label: 'Pools', icon: Boxes, key: 'p' },
    { path: '/runners', label: 'Runners', icon: Server, key: 'r' },
    { path: '/jobs', label: 'Jobs', icon: ListChecks, key: 'j' },
    { path: '/hosts', label: 'Hosts', icon: HardDrive, key: 'h' },
    { path: '/installations', label: 'Installations', icon: Plug, key: 'i' },
    { path: '/migrate', label: 'Migrate', icon: GitPullRequestArrow, key: 'm' },
    { path: '/audit', label: 'Audit', icon: ScrollText, key: 'a' },
    { path: '/settings', label: 'Settings', icon: Settings, key: 's' },
  ];

  const collapsed = $derived(prefs.navCollapsed);

  function isCurrent(path: string): boolean {
    const here = router.pathname;
    return path === '/' ? here === '/' : here === path || here.startsWith(`${path}/`);
  }
</script>

<nav class="nav" class:collapsed aria-label="Sections">
  <div class="brand">
    <a href="/" class="mark" aria-label="Zoomies, go to the overview">
      <Logo variant="mark" size={32} label="" />
      {#if !collapsed}<span class="brand-name">Zoomies</span>{/if}
    </a>
    {#if !collapsed}
      <p class="descriptor">Self-hosted Git runners</p>
    {/if}
  </div>

  <ul>
    {#each items as item (item.path)}
      {@const current = isCurrent(item.path)}
      <li>
        <a
          href={item.path}
          aria-current={current ? 'page' : undefined}
          class:current
          title={collapsed ? item.label : undefined}
        >
          <item.icon size={16} aria-hidden="true" />
          {#if !collapsed}
            <span class="label">{item.label}</span>
            <kbd aria-hidden="true">g {item.key}</kbd>
          {:else}
            <span class="sr-only">{item.label}</span>
          {/if}
        </a>
      </li>
    {/each}
  </ul>

  <div class="foot">
    <IconButton
      icon={collapsed ? PanelLeftOpen : PanelLeftClose}
      label={collapsed ? 'Expand the navigation' : 'Collapse the navigation'}
      size="sm"
      onclick={() => prefs.toggleNav()}
    />
  </div>
</nav>

<style>
  .nav {
    position: sticky;
    top: 0;
    display: flex;
    flex-direction: column;
    width: var(--z-nav-width);
    height: 100vh;
    padding: var(--z-space-3);
    border-right: 1px solid var(--z-border);
    background: var(--z-surface);
    z-index: var(--z-layer-nav);
    transition: width var(--z-motion-base) var(--z-ease);
  }
  .nav.collapsed {
    width: var(--z-nav-width-collapsed);
    padding-inline: var(--z-space-2);
  }
  /*
    The masthead is too small for the detailed dog artwork to read cleanly, so
    it uses the paw/swish shorthand at 32px and sets the product name as
    ordinary interface text. It does not imitate or crop the wordmark.
  */
  .brand {
    padding: var(--z-space-2) var(--z-space-2) var(--z-space-3);
    margin-bottom: var(--z-space-3);
    border-bottom: 1px solid var(--z-border);
  }
  .collapsed .brand {
    display: flex;
    justify-content: center;
    padding-inline: 0;
  }
  .mark {
    display: inline-flex;
    align-items: center;
    color: var(--z-text);
    text-decoration: none;
    border-radius: var(--z-radius-md);
  }
  .mark:hover {
    /* The mark is fixed artwork; the text carries the hover state. */
    color: var(--z-accent);
  }
  .brand-name {
    margin-left: var(--z-space-3);
    font-size: var(--z-text-xl);
    line-height: 1;
    font-weight: var(--z-weight-semibold);
    letter-spacing: -0.02em;
  }
  /*
    Set in Inter, not taken from the artwork: the descriptor line inside the
    full logo is not separated or cropped for compact UI.
  */
  .descriptor {
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-2xs);
    line-height: 1.4;
    font-weight: var(--z-weight-medium);
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--z-text-subtle);
  }
  ul {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
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
    transition:
      background-color var(--z-motion-fast) var(--z-ease),
      color var(--z-motion-fast) var(--z-ease);
  }
  .collapsed a {
    justify-content: center;
    padding: 0;
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
    white-space: nowrap;
  }
  kbd {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
    opacity: 0;
    transition: opacity var(--z-motion-fast) var(--z-ease);
  }
  a:hover kbd,
  a:focus-visible kbd {
    opacity: 1;
  }
  .foot {
    display: flex;
    justify-content: flex-end;
    padding-top: var(--z-space-2);
  }
  .collapsed .foot {
    justify-content: center;
  }
  @media (max-width: 768px) {
    .nav {
      position: fixed;
      inset-block: auto 0;
      inset-inline: 0;
      flex-direction: row;
      align-items: center;
      width: 100%;
      height: auto;
      padding: var(--z-space-1);
      border-right: 0;
      border-top: 1px solid var(--z-border);
    }
    .brand,
    .foot {
      display: none;
    }
    ul {
      flex-direction: row;
      justify-content: space-around;
      width: 100%;
    }
    a {
      flex-direction: column;
      gap: 2px;
      height: var(--z-space-12);
      padding: 0 var(--z-space-1);
    }
    kbd {
      display: none;
    }
    /*
      Eight labels do not fit across a phone, but `display: none` would take
      each entry's accessible name with them and a screen reader would
      announce eight links called "link". Hidden the way the collapsed desktop
      nav hides its own labels instead: off the screen, still in the
      accessibility tree.
    */
    .label {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
      border-width: 0;
    }
  }
</style>
