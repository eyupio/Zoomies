<!--
  Eli, floating at the corner of every page.

  A round button opens a panel with the conversation. The panel is not a modal:
  the page behind stays usable, Escape and the close button put it away and give
  the keyboard back to the button, and the conversation is the one kept in
  `eli`, so it is still there on the next page. On a phone the panel takes the
  whole screen, above the navigation, because a chat in a corner of a phone is
  not one anybody can read.

  It is mounted only when there is somebody to ask, so nothing here checks.
-->
<script lang="ts">
  import { PawPrint, RotateCcw, Settings, X } from '@lucide/svelte';
  import { tick } from 'svelte';
  import { router } from '$lib/router';
  import IconButton from '$lib/components/IconButton.svelte';
  import ConversationView from './ConversationView.svelte';
  import EliAvatar from './EliAvatar.svelte';
  import { eli } from './eli.svelte';

  let launcher = $state<HTMLButtonElement>();
  let panel = $state<HTMLElement>();

  const answering = $derived(eli.answering);

  // Opening puts the keyboard in the box, which is the one thing anybody opens
  // Eli to do. It runs on the way in only: closing is handled where it happens.
  let wasOpen = false;
  $effect(() => {
    const open = eli.open;
    if (open && !wasOpen) {
      void tick().then(() =>
        panel?.querySelector<HTMLElement>('textarea:not([disabled])')?.focus(),
      );
    }
    wasOpen = open;
  });

  function close(): void {
    eli.hide();
    void tick().then(() => launcher?.focus());
  }

  function onkeydown(event: KeyboardEvent): void {
    if (event.key !== 'Escape') return;
    // This is the panel's own Escape: the shell's would close an overlay above
    // it, and there is none when focus is in here.
    event.stopPropagation();
    event.preventDefault();
    close();
  }
</script>

{#if eli.open && answering}
  <!-- Escape is handled on the panel, where focus is, and not on the window. -->
  <div
    class="panel"
    id="eli-panel"
    role="dialog"
    tabindex="-1"
    aria-label="Eli"
    bind:this={panel}
    {onkeydown}
  >
    <header>
      <EliAvatar size={32} />
      <div class="who">
        <h2><abbr title="Extremely Lively Intelligence">Eli</abbr></h2>
        <p>Answers from <strong>{answering.name}</strong>, {answering.model}</p>
      </div>
      <div class="actions">
        {#if eli.conversation.turns.length > 0}
          <IconButton
            icon={RotateCcw}
            label="New conversation"
            size="sm"
            onclick={() => eli.conversation.clear()}
          />
        {/if}
        <IconButton
          icon={Settings}
          label="Assistant settings"
          size="sm"
          onclick={() => router.navigate('/settings/assistant')}
        />
        <IconButton icon={X} label="Close Eli" size="sm" onclick={close} />
      </div>
    </header>

    {#if !eli.fleetAccess}
      <p class="notice">
        Eli cannot see this fleet through {answering.name}. An administrator can allow it in
        <a
          href="/settings/assistant"
          onclick={(event) => {
            event.preventDefault();
            router.navigate('/settings/assistant');
          }}>Settings, Assistant</a
        >.
      </p>
    {/if}

    <div class="body">
      <ConversationView
        conversation={eli.conversation}
        answering={{ id: answering.id, name: answering.name, model: answering.model }}
        fleetAccess={eli.fleetAccess}
        height="100%"
      />
    </div>
  </div>
{/if}

<button
  type="button"
  class="launcher"
  class:open={eli.open}
  bind:this={launcher}
  aria-label="Ask Eli"
  aria-expanded={eli.open}
  aria-controls="eli-panel"
  title="Eli, Extremely Lively Intelligence (E)"
  onclick={() => eli.toggle()}
>
  <PawPrint size={22} aria-hidden="true" />
</button>

<style>
  /*
    Sizes here are this widget's own layout and not the product's scale: a launcher
    and a panel are measured against the screen they float on. The phone offset
    clears the bottom navigation, which is about this tall plus the home indicator.
  */
  .launcher,
  .panel {
    position: fixed;
    z-index: var(--z-layer-dropdown);
    right: var(--z-space-4);
  }
  .launcher {
    bottom: calc(var(--z-space-4) + var(--z-safe-bottom));
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 3.25rem;
    height: 3.25rem;
    color: var(--z-accent-contrast);
    background: var(--z-accent);
    border: 0;
    border-radius: var(--z-radius-full);
    box-shadow: var(--z-shadow-lg);
    cursor: pointer;
    transition:
      background var(--z-motion-fast) var(--z-ease),
      transform var(--z-motion-fast) var(--z-ease);
  }
  .launcher:hover {
    background: var(--z-accent-hover);
    transform: scale(1.05);
  }
  .launcher:active {
    background: var(--z-accent-active);
  }
  .launcher.open {
    background: var(--z-surface-raised);
    color: var(--z-accent);
    border: var(--z-border-width) solid var(--z-border-strong);
  }
  @media (prefers-reduced-motion: reduce) {
    .launcher {
      transition: none;
    }
    .launcher:hover {
      transform: none;
    }
  }

  .panel {
    bottom: calc(var(--z-space-4) + 3.25rem + var(--z-space-2) + var(--z-safe-bottom));
    display: flex;
    flex-direction: column;
    width: min(26rem, calc(100vw - 2 * var(--z-space-4)));
    height: min(40rem, calc(100dvh - 9rem));
    background: var(--z-surface-raised);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-lg);
    box-shadow: var(--z-shadow-lg);
    overflow: hidden;
  }
  header {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-3) var(--z-space-3) var(--z-space-4);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .who {
    flex: 1;
    min-width: 0;
  }
  h2,
  p {
    margin: 0;
  }
  h2 {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  abbr {
    text-decoration: none;
    cursor: help;
  }
  .who p {
    overflow: hidden;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .actions {
    display: flex;
    gap: var(--z-space-1);
  }
  .notice {
    padding: var(--z-space-2) var(--z-space-4);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    background: var(--z-surface-sunken);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .notice a {
    color: var(--z-accent);
  }
  .body {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-height: 0;
    padding: var(--z-space-3);
  }
  .body :global(.view) {
    flex: 1;
  }

  @media (max-width: 768px) {
    .launcher {
      bottom: calc(5rem + var(--z-safe-bottom));
    }
    .launcher.open {
      display: none;
    }
    .panel {
      inset: 0;
      width: auto;
      height: auto;
      border: 0;
      border-radius: 0;
      padding-top: var(--z-safe-top);
      padding-bottom: var(--z-safe-bottom);
    }
  }
</style>
