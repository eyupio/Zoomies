<!--
  The top bar: where you are, whether the data is live, and the two things every
  page needs -- the palette and the account menu.

  The connection state is never silent. A quiet dot when live; the word
  "Reconnecting" when not. An operator must never be looking at a frozen screen
  that claims to be live.

  The problems bell is here for the same reason: whatever page an operator is
  on, the count of things that need them is on screen, and the list is one click
  away rather than only on the Overview.

  The account menu is about the person: who they are signed in as, the theme,
  their own settings pages, the shortcuts and the way out. The theme used to be
  an icon of its own beside the menu that cycled through three states on each
  press and said which one only in its tooltip; it is a choice inside the menu
  now, where the three options are visible and the one in force is marked.
-->
<script lang="ts">
  import {
    Activity,
    BookOpen,
    CircleUser,
    Keyboard,
    KeyRound,
    LogOut,
    Monitor,
    Moon,
    Palette,
    Search,
    Sun,
    User,
  } from '@lucide/svelte';
  import { modKey } from '../keys';
  import { DOCS_URL } from '../links';
  import { roleLabel } from '../roles';
  import { router } from '../router';
  import { fleet } from '../state/fleet.svelte';
  import { session } from '../state/session.svelte';
  import { theme } from '../state/theme.svelte';
  import ProblemsBell from '../problems/ProblemsBell.svelte';
  import DropdownMenu from '../components/DropdownMenu.svelte';
  import Logo from '../components/Logo.svelte';
  import type { MenuItem } from '../components/DropdownMenu.svelte';
  import { SECTIONS, isCurrentSection } from './sections';
  import { signOut } from './signout';

  interface Props {
    onpalette: () => void;
    onshortcuts: () => void;
  }

  let { onpalette, onshortcuts }: Props = $props();

  const connection = $derived(fleet.connection);

  const connectionText = $derived(
    connection === 'live'
      ? 'Live'
      : connection === 'connecting'
        ? 'Connecting'
        : connection === 'reconnecting'
          ? 'Reconnecting'
          : 'Offline',
  );

  const connectionHint = $derived(
    connection === 'live'
      ? 'Updates are arriving as they happen.'
      : connection === 'offline'
        ? 'No connection to the controller. The page will catch up by itself once it returns.'
        : 'Trying to reach the controller. What you see may be a few seconds old.',
  );

  /**
   * The section the page is inside, when the page has a name of its own: a
   * settings page or a runner's detail. It is shown as the crumb before the
   * page name, so the bar reads "Settings › Users" rather than "Users" alone.
   */
  const section = $derived(SECTIONS.find((item) => isCurrentSection(item.path, router.pathname)));
  const crumb = $derived(
    section &&
      section.path !== '/' &&
      router.pathname !== section.path &&
      router.title !== section.label
      ? section.label
      : null,
  );

  const initial = $derived(session.displayName.trim().charAt(0).toUpperCase() || '?');
  const roleLine = $derived(
    session.authDisabled
      ? 'Authentication is off, so every request is an administrator'
      : roleLabel(session.role),
  );

  const menuItems = $derived<MenuItem[]>([
    {
      id: 'theme-system',
      label: 'System',
      choice: 'Theme',
      checked: theme.choice === 'system',
      icon: Monitor,
      onSelect: () => theme.set('system'),
    },
    {
      id: 'theme-light',
      label: 'Light',
      choice: 'Theme',
      checked: theme.choice === 'light',
      icon: Sun,
      onSelect: () => theme.set('light'),
    },
    {
      id: 'theme-dark',
      label: 'Dark',
      choice: 'Theme',
      checked: theme.choice === 'dark',
      icon: Moon,
      onSelect: () => theme.set('dark'),
    },
    {
      id: 'account',
      label: 'Your account',
      icon: CircleUser,
      separated: true,
      onSelect: () => router.navigate('/settings/account'),
    },
    // Tokens are an administrator's to manage; the menu offers the page only
    // to someone the page will not refuse.
    ...(session.can('admin')
      ? [
          {
            id: 'tokens',
            label: 'API tokens',
            icon: KeyRound,
            onSelect: () => router.navigate('/settings/tokens'),
          },
        ]
      : []),
    {
      id: 'appearance',
      label: 'Appearance',
      icon: Palette,
      onSelect: () => router.navigate('/settings/appearance'),
    },
    {
      id: 'events',
      label: 'Events',
      icon: Activity,
      onSelect: () => router.navigate('/settings/events'),
    },
    {
      id: 'shortcuts',
      label: 'Keyboard shortcuts',
      icon: Keyboard,
      hint: '?',
      onSelect: onshortcuts,
    },
    {
      id: 'docs',
      label: 'Documentation',
      icon: BookOpen,
      href: DOCS_URL,
      newTab: true,
    },
    {
      id: 'signout',
      label: 'Sign out',
      icon: LogOut,
      separated: true,
      disabled: session.authDisabled,
      onSelect: () => void signOut(),
    },
  ]);
</script>

<header class="topbar">
  <!--
    On a phone the navigation is a bar along the bottom and its masthead is
    gone, so this is the only place the product says its own name. It is a link
    home for the same reason the masthead is.
  -->
  <a href="/" class="home" aria-label="Zoomies, go to the overview">
    <Logo variant="mark" size={22} label="" />
  </a>

  <div class="title">
    <p class="page">
      {#if crumb}
        <span class="crumb">{crumb}</span>
        <span class="crumb-sep" aria-hidden="true">›</span>
      {/if}
      <span>{router.title}</span>
    </p>
  </div>

  <div class="right">
    <p class="connection" data-state={connection} title={connectionHint}>
      <span class="dot" aria-hidden="true"></span>
      <span class="connection-text" class:sr-only={connection === 'live'}>{connectionText}</span>
    </p>
    <output class="sr-only" aria-live="polite">Connection: {connectionText}</output>

    <ProblemsBell />

    <!--
      The label is on the button rather than only in it: below the sidebar
      breakpoint the words are hidden, and below the phone breakpoint the key
      cap goes too, so without this the button's accessible name would narrow
      to "Ctrl K" and then to nothing at all.
    -->
    <button type="button" class="palette-hint" aria-label="Search or jump to" onclick={onpalette}>
      <Search size={13} aria-hidden="true" />
      <span>Search or jump to</span>
      <kbd>{modKey} K</kbd>
    </button>

    <DropdownMenu
      items={menuItems}
      label="Account menu"
      triggerLabel={session.displayName}
      triggerIcon={User}
      size="sm"
      align="end"
    >
      {#snippet header()}
        <div class="who">
          <span class="avatar" aria-hidden="true">{initial}</span>
          <div class="who-text">
            <p class="who-name">{session.displayName}</p>
            <p class="who-role">{roleLine}</p>
          </div>
        </div>
      {/snippet}
    </DropdownMenu>
  </div>
</header>

<style>
  .topbar {
    position: sticky;
    top: 0;
    z-index: var(--z-layer-sticky);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-4);
    height: var(--z-topbar-height);
    padding: 0 var(--z-space-6);
    border-bottom: var(--z-border-width) solid var(--z-border);
    background: color-mix(in srgb, var(--z-bg) 88%, transparent);
    backdrop-filter: blur(8px);
  }
  /* Desktop has the masthead in the sidebar; two marks on one screen is one
     too many. */
  .home {
    display: none;
    flex: none;
    align-items: center;
    border-radius: var(--z-radius-md);
    color: var(--z-text);
    text-decoration: none;
  }
  .title {
    /*
      The bar must never be wider than the window: on a phone that is the
      difference between a page that fits and a page the browser zooms out to
      fit, whose controls are then too small to press. The title is the part
      that gives way, because the h1 below it says the same thing.
    */
    flex: 1;
    min-width: 0;
  }
  .page {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow: hidden;
    white-space: nowrap;
  }
  .page > span {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .crumb,
  .crumb-sep {
    flex: none;
    font-weight: var(--z-weight-medium);
    color: var(--z-text-subtle);
  }
  .right {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    flex: none;
  }
  .connection {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    cursor: help;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: var(--z-radius-full);
    background: var(--z-neutral);
  }
  .connection[data-state='live'] .dot {
    background: var(--z-idle);
  }
  .connection[data-state='connecting'] .dot,
  .connection[data-state='reconnecting'] .dot {
    background: var(--z-pending);
    animation: pulse calc(var(--z-motion-slow) * 4) var(--z-ease) infinite;
  }
  .connection[data-state='offline'] .dot {
    background: var(--z-danger);
  }
  .connection[data-state='reconnecting'] .connection-text,
  .connection[data-state='connecting'] .connection-text {
    color: var(--z-pending);
  }
  .connection[data-state='offline'] .connection-text {
    color: var(--z-danger);
  }
  @keyframes pulse {
    50% {
      opacity: 0.35;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    /* The colour and the word "Reconnecting" carry the state on their own. */
    .connection .dot {
      animation: none;
    }
  }
  .palette-hint {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    height: var(--z-space-6);
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    color: var(--z-text-subtle);
    font-family: inherit;
    font-size: var(--z-text-xs);
    cursor: pointer;
  }
  .palette-hint:hover {
    border-color: var(--z-border-strong);
    color: var(--z-text-muted);
  }
  kbd {
    padding: var(--z-nudge-1) var(--z-space-1);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
  }
  .who {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    min-width: 16rem;
  }
  .avatar {
    display: inline-grid;
    place-items: center;
    flex: none;
    width: var(--z-space-8);
    height: var(--z-space-8);
    border-radius: var(--z-radius-full);
    background: var(--z-accent-subtle);
    color: var(--z-accent);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
  }
  .who-text {
    min-width: 0;
  }
  .who-name {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .who-role {
    margin: 0;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-subtle);
  }
  @media (max-width: 1180px) {
    .palette-hint span {
      display: none;
    }
  }
  @media (max-width: 768px) {
    /*
      The whole hint goes on a phone. Hiding only its text left a `Ctrl K`
      key cap on a device with no Ctrl and no K -- an instruction for a
      keyboard that is not there. The palette is still reachable: the hint is
      a button, and the search icon beside it opens it.
    */
    .palette-hint kbd {
      display: none;
    }
    .topbar {
      gap: var(--z-space-3);
      padding: 0 var(--z-space-3);
    }
    .home {
      display: inline-flex;
    }
    /*
      "Authentication disabled" -- or any long display name -- is 175px of a
      412px phone. The trigger keeps its accessible name ("Account menu") and
      its icon; only the word beside the icon goes.
    */
    .right :global(.trigger-label) {
      display: none;
    }
  }
</style>
