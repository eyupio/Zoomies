<!--
  The phone's menu: every section, named, in a sheet that rises from the
  bottom edge -- where the thumb that pressed More already is.

  The bar along the bottom edge carries four of the twelve sections, which is
  as many as fit a screen as labelled targets. This is where the whole map
  lives, in the same fixed order, with the one being looked at marked. It is
  also where the phone keeps what the desktop puts in the sidebar's masthead
  and the top bar's account menu: the product's name, who is signed in, the
  theme, and the way out.

  It behaves like the rest of the overlays: a modal layer, Escape closes it, the
  page behind it is inert, focus is trapped and given back. It closes itself
  the moment a section is chosen -- nobody wants to dismiss a menu they have
  already used.
-->
<script lang="ts">
  import { LogOut, X } from '@lucide/svelte';
  import { layers, lockScroll, pageInert, trapFocus } from '../keys';
  import { router } from '../router';
  import { roleLabel } from '../roles';
  import { session } from '../state/session.svelte';
  import { theme, THEME_OPTIONS } from '../state/theme.svelte';
  import type { ThemeChoice } from '../state/theme.svelte';
  import { viewport } from '../state/viewport.svelte';
  import Button from '../components/Button.svelte';
  import IconButton from '../components/IconButton.svelte';
  import Logo from '../components/Logo.svelte';
  import Segmented from '../components/Segmented.svelte';
  import { SECTIONS, isCurrentSection } from './sections';
  import BuildTag from './BuildTag.svelte';
  import { signOut } from './signout';

  interface Props {
    open?: boolean;
  }

  let { open = $bindable(false) }: Props = $props();

  let backdrop = $state<HTMLDivElement | null>(null);

  const initial = $derived(session.displayName.trim().charAt(0).toUpperCase() || '?');
  const roleLine = $derived(
    session.authDisabled ? 'Authentication is off' : roleLabel(session.role),
  );

  function close(): void {
    open = false;
  }

  function isCurrent(path: string): boolean {
    return isCurrentSection(path, router.pathname);
  }

  // A window that has grown past the phone breakpoint has its sidebar back, and
  // a sheet floating over that is a second navigation saying the same thing.
  $effect(() => {
    if (!viewport.phone) open = false;
  });

  $effect(() => {
    if (!open) return;
    const layer = layers.push('drawer', close);
    const unlock = lockScroll();
    const uninert = pageInert(backdrop);
    return () => {
      layers.remove(layer);
      unlock();
      uninert();
    };
  });
</script>

{#if open}
  <div class="backdrop" bind:this={backdrop}>
    <!-- A div, not a focusable-but-aria-hidden button. See Dialog.svelte. -->
    <div class="scrim" aria-hidden="true" onclick={close}></div>
    <div class="sheet" role="dialog" aria-modal="true" aria-label="All sections" use:trapFocus>
      <span class="handle" aria-hidden="true"></span>
      <header>
        <a href="/" class="mark" onclick={close}>
          <Logo variant="mark" size={28} label="" />
          <span class="name">Zoomies</span>
        </a>
        <IconButton icon={X} label="Close the menu" size="sm" onclick={close} />
      </header>

      <div class="who">
        <span class="avatar" aria-hidden="true">{initial}</span>
        <div class="who-text">
          <p class="who-name">{session.displayName}</p>
          <p class="who-role">{roleLine}</p>
        </div>
      </div>

      <div class="theme">
        <span class="theme-label" aria-hidden="true">Theme</span>
        <Segmented
          options={THEME_OPTIONS}
          value={theme.choice}
          label="Theme"
          onchange={(value) => theme.set(value as ThemeChoice)}
        />
      </div>

      <!--
        Named for what it is rather than "Sections", which the bottom bar
        already answers to: two navigation landmarks sharing one name is two
        landmarks a screen-reader user cannot tell apart.
      -->
      <nav aria-label="All sections">
        <ul>
          {#each SECTIONS as item (item.path)}
            {@const current = isCurrent(item.path)}
            <li>
              <a
                href={item.path}
                aria-current={current ? 'page' : undefined}
                class:current
                onclick={close}
              >
                <item.icon size={20} aria-hidden="true" />
                <span>{item.label}</span>
              </a>
            </li>
          {/each}
        </ul>
      </nav>

      <div class="foot">
        <Button
          variant="ghost"
          icon={LogOut}
          disabled={session.authDisabled}
          onclick={() => void signOut()}
        >
          Sign out
        </Button>
        <BuildTag />
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    /* The window, not the document: see --z-window-width. */
    inset: 0 auto 0 0;
    width: var(--z-window-width);
    z-index: var(--z-layer-drawer);
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
  }
  .scrim {
    position: absolute;
    inset: 0;
    background: var(--z-scrim);
    backdrop-filter: blur(var(--z-scrim-blur));
    cursor: default;
  }
  .sheet {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    max-height: calc(100% - var(--z-space-12));
    overflow-y: auto;
    /* The same protected bottom edge as the bar this sheet covers. */
    padding: var(--z-space-2) var(--z-space-4) calc(var(--z-space-4) + var(--z-safe-bottom));
    border-top: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-lg) var(--z-radius-lg) 0 0;
    background: var(--z-surface);
    box-shadow: var(--z-shadow-lg);
    animation: rise var(--z-motion-slow) var(--z-ease);
  }
  .sheet:focus {
    outline: none;
  }
  .handle {
    display: block;
    width: var(--z-space-10);
    height: var(--z-space-1);
    margin: 0 auto;
    border-radius: var(--z-radius-full);
    background: var(--z-border-strong);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-4);
  }
  .mark {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-3);
    color: var(--z-text);
    text-decoration: none;
    border-radius: var(--z-radius-md);
  }
  .name {
    font-size: var(--z-text-lg);
    font-weight: var(--z-weight-semibold);
    letter-spacing: -0.02em;
  }
  .who {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
  }
  .avatar {
    display: inline-grid;
    place-items: center;
    flex: none;
    width: var(--z-space-10);
    height: var(--z-space-10);
    border-radius: var(--z-radius-full);
    background: var(--z-accent-subtle);
    color: var(--z-accent);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .who-text {
    min-width: 0;
  }
  .who-name {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .who-role {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .theme {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding-bottom: var(--z-space-3);
    border-bottom: var(--z-border-width) solid var(--z-border);
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  ul {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  /*
    Scoped to the list: the masthead link above it is an anchor too, and a
    bare `a` rule turned it into a tile with the name under the mark.
  */
  nav a {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--z-space-1);
    height: var(--z-space-16);
    padding: 0 var(--z-space-1);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    text-decoration: none;
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    line-height: 1;
  }
  nav a span {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  nav a:hover {
    background: var(--z-surface-hover);
    color: var(--z-text);
  }
  nav a.current {
    background: var(--z-accent-subtle);
    color: var(--z-accent);
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding-top: var(--z-space-2);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  @keyframes rise {
    from {
      transform: translateY(var(--z-space-4));
      opacity: 0;
    }
  }
</style>
