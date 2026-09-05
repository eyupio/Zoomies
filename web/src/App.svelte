<!--
  The application shell.

  Boot order: ask the server what it is, then who we are, then render one of
  three things -- the first-run form, the login form, or the product. The SSE
  connection is opened only once there is somebody to show it to.
-->
<script lang="ts">
  import { onMount, tick, untrack } from 'svelte';
  import { onUnauthorized } from '$lib/api/client';
  import { installShortcuts, focusSearch } from '$lib/keys';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Toaster from '$lib/components/Toaster.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import AppFooter from '$lib/shell/AppFooter.svelte';
  import CommandPalette from '$lib/shell/CommandPalette.svelte';
  import Nav from '$lib/shell/Nav.svelte';
  import ProblemsDrawer from '$lib/problems/ProblemsDrawer.svelte';
  import ShortcutSheet from '$lib/shell/ShortcutSheet.svelte';
  import TopBar from '$lib/shell/TopBar.svelte';
  import Bootstrap from './routes/Bootstrap.svelte';
  import Login from './routes/Login.svelte';

  let paletteOpen = $state(false);
  let shortcutsOpen = $state(false);
  let announcement = $state('');
  let warnedAboutPassword = false;

  const Page = $derived(router.component);
  const authenticated = $derived(session.phase === 'ready');

  onMount(() => {
    onUnauthorized(() => {
      session.clear();
      fleet.stop();
      if (router.pathname !== '/login') router.navigate('/login');
    });
    router.start();
    void session.boot();

    return installShortcuts({
      palette: () => (paletteOpen = true),
      help: () => (shortcutsOpen = true),
      search: () => {
        // A page with no search of its own falls back to the palette, which can
        // search everything.
        if (!focusSearch()) paletteOpen = true;
      },
      go: (path) => router.navigate(path),
    });
  });

  // Connect the live stream exactly once, and only for somebody signed in.
  $effect(() => {
    if (authenticated) fleet.start();
  });

  // An administrator has reset this password; say so once rather than letting
  // the operator discover it at the worst moment.
  $effect(() => {
    if (authenticated && session.mustChangePassword && !warnedAboutPassword) {
      warnedAboutPassword = true;
      toasts.push({
        tone: 'warning',
        title: 'Your password must be changed',
        message:
          'It was reset by an administrator. Change it in Settings before doing anything else.',
        timeout: 0,
        action: { label: 'Open settings', run: () => router.navigate('/settings') },
      });
    }
  });

  /**
   * Route change: move focus to the page heading so a keyboard user lands on
   * the content rather than at the top of the navigation, and announce the page
   * name politely for anyone who cannot see that it changed.
   */
  $effect(() => {
    const count = router.navigation;
    // The title is read untracked: a detail page renaming itself once it knows
    // what it is looking at must not steal focus back to the heading.
    const title = untrack(() => router.title);
    if (!authenticated || count === 0) return;
    void tick().then(() => {
      const heading = document.getElementById('page-heading') ?? document.getElementById('main');
      heading?.focus();
      announcement = `${title}. Page loaded.`;
    });
  });
</script>

<a class="skip-link" href="#main">Skip to the main content</a>

{#if session.phase === 'booting'}
  <div class="boot" aria-busy="true">
    <Logo variant="lockup" size={96} label="" />
    <span class="sr-only">Loading Zoomies</span>
  </div>
{:else if session.phase === 'failed'}
  <main id="main" class="centred">
    <div class="brand"><Logo variant="lockup" size={80} label="" /></div>
    <ErrorState
      error={session.error}
      title="Cannot reach the Zoomies controller"
      description={session.error?.message ??
        'The controller did not answer. Check that the process is running and that this address is right.'}
      onretry={() => void session.boot()}
    />
  </main>
{:else if session.phase === 'bootstrap'}
  <main id="main" class="centred"><Bootstrap /></main>
{:else if !authenticated}
  <main id="main" class="centred"><Login /></main>
{:else}
  <div class="app">
    <Nav />
    <div class="column">
      <TopBar onpalette={() => (paletteOpen = true)} onshortcuts={() => (shortcutsOpen = true)} />
      <main id="main" tabindex="-1">
        {#if router.error}
          <div class="page">
            <ErrorState
              error={router.error}
              title="That page could not be loaded"
              description="The page's code failed to download. Check your connection, then try again."
              onretry={() => location.reload()}
            />
          </div>
        {:else if router.loading || !Page}
          <div class="page" aria-busy="true">
            <Skeleton width="220px" height="1.75rem" />
            <div class="page-skeleton">
              <Skeleton height="88px" />
              <Skeleton height="88px" />
              <Skeleton height="88px" />
              <Skeleton height="88px" />
            </div>
            <Skeleton height="240px" />
          </div>
        {:else}
          <div class="page">
            <Page />
          </div>
        {/if}
      </main>
      <AppFooter />
    </div>
  </div>

  <CommandPalette bind:open={paletteOpen} />
  <ProblemsDrawer />
  <ShortcutSheet bind:open={shortcutsOpen} />
{/if}

<output class="sr-only" aria-live="polite">{announcement}</output>
<Toaster />

<style>
  .boot,
  .centred {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--z-space-3);
    min-height: 100vh;
    padding: var(--z-space-6);
  }
  /*
    The boot screen is usually gone within a frame or two, and a logo that
    flashes on and then straight off again is worse than no logo at all. So it
    waits out a third of a second and fades in only for the boots that are
    actually slow enough to be worth reassuring somebody about. Under
    reduced motion the durations collapse to 1ms with the rest of the tokens,
    which shows it immediately -- correct, and one less rule to keep in step.
  */
  .boot :global(.logo) {
    opacity: 0;
    animation: reveal var(--z-motion-slow) var(--z-ease) var(--z-motion-slow) forwards;
  }
  @keyframes reveal {
    to {
      opacity: 1;
    }
  }
  .brand {
    margin-bottom: var(--z-space-2);
    color: var(--z-text);
  }
  .app {
    display: flex;
    align-items: flex-start;
    min-height: 100vh;
  }
  .column {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    min-height: 100vh;
  }
  main {
    flex: 1;
    min-width: 0;
  }
  main:focus {
    outline: none;
  }
  .page {
    max-width: var(--z-content-max);
    margin: 0 auto;
    padding: var(--z-space-6);
  }
  .page-skeleton {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: var(--z-space-4);
    margin: var(--z-space-6) 0;
  }
  @media (max-width: 768px) {
    .app {
      flex-direction: column;
      /*
        The row becomes a column here, so `align-items: flex-start` stops
        meaning "the sidebar keeps its width" and starts meaning "the content
        column is as wide as its widest child". A 1150px grid, or a top-bar
        label a few pixels too long, then sizes the whole document and the
        phone scrolls sideways -- which the browser answers by zooming out
        until the toolbar and the row actions are too small to press.
      */
      align-items: stretch;
    }
    .page {
      /* The clearance for the fixed bottom navigation is the footer's job
         below this, so the page itself does not also reserve it and leave a
         hole between the content and the footer. */
      padding: var(--z-space-4) var(--z-space-3) var(--z-space-2);
    }
  }
</style>
