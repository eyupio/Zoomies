<!--
  The top bar: where you are, whether the data is live, and the two things every
  page needs -- the palette and the account menu.

  The connection state is never silent. A quiet dot when live; the word
  "Reconnecting" when not. An operator must never be looking at a frozen screen
  that claims to be live.
-->
<script lang="ts">
  import { Keyboard, LogOut, Monitor, Moon, Search, Sun, User } from '@lucide/svelte';
  import { modKey } from '../keys';
  import { router } from '../router';
  import { fleet } from '../state/fleet.svelte';
  import { session } from '../state/session.svelte';
  import { theme } from '../state/theme.svelte';
  import { toasts } from '../state/toasts.svelte';
  import DropdownMenu from '../components/DropdownMenu.svelte';
  import IconButton from '../components/IconButton.svelte';
  import Logo from '../components/Logo.svelte';
  import type { MenuItem } from '../components/DropdownMenu.svelte';

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

  const themeIcon = $derived(
    theme.choice === 'light' ? Sun : theme.choice === 'dark' ? Moon : Monitor,
  );
  const themeLabel = $derived(
    `Theme: ${theme.choice}. Switch to ${
      theme.choice === 'light' ? 'dark' : theme.choice === 'dark' ? 'system' : 'light'
    }`,
  );

  const menuItems = $derived<MenuItem[]>([
    {
      id: 'shortcuts',
      label: 'Keyboard shortcuts',
      icon: Keyboard,
      onSelect: onshortcuts,
    },
    {
      id: 'account',
      label: 'Account and settings',
      icon: User,
      onSelect: () => router.navigate('/settings'),
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

  async function signOut(): Promise<void> {
    try {
      fleet.stop();
      await session.logout();
      router.navigate('/login');
    } catch (cause) {
      toasts.fromError(cause, 'Could not sign out');
    }
  }
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
    <p class="page">{router.title}</p>
  </div>

  <div class="right">
    <p class="connection" data-state={connection} title={connectionHint}>
      <span class="dot" aria-hidden="true"></span>
      <span class="connection-text" class:sr-only={connection === 'live'}>{connectionText}</span>
    </p>
    <output class="sr-only" aria-live="polite">Connection: {connectionText}</output>

    <button type="button" class="palette-hint" onclick={onpalette}>
      <Search size={13} aria-hidden="true" />
      <span>Search or jump to</span>
      <kbd>{modKey} K</kbd>
    </button>

    <IconButton icon={themeIcon} label={themeLabel} size="sm" onclick={() => theme.cycle()} />

    <DropdownMenu
      items={menuItems}
      label="Account menu"
      triggerLabel={session.displayName}
      triggerIcon={User}
      size="sm"
      align="end"
    />
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
    border-bottom: 1px solid var(--z-border);
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
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
    border: 1px solid var(--z-border);
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
    padding: 1px var(--z-space-1);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
  }
  @media (max-width: 1180px) {
    .palette-hint span {
      display: none;
    }
  }
  @media (max-width: 768px) {
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
