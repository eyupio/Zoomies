<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { Maximize2, Minimize2, PanelLeft, PanelRight, SquarePen, X } from '@lucide/svelte';
  import { listAssistantProviders } from '$lib/api/client';
  import Button from '$lib/components/Button.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import ConversationView from './ConversationView.svelte';
  import EliAvatar from './EliAvatar.svelte';
  import { movable } from './movable';
  import { eli } from './eli.svelte';
  import { contextPrompt } from './prompts';
  import { layers, lockScroll, pageInert, trapFocus } from '$lib/keys';
  import { router } from '$lib/router';

  let phone = $state(false);
  let size = $state('comfortable');
  let side = $state('right');
  let full = $state(false);
  const modal = $derived(full || phone);
  let loading = $state(false);
  const answering = $derived(loading ? undefined : eli.answering);
  let error = $state('');
  let panel = $state<HTMLElement>();
  let request: AbortController | undefined;
  onMount(() => {
    const media = window.matchMedia('(max-width: 767px)');
    const resized = () => {
      phone = media.matches;
    };
    resized();
    media.addEventListener('change', resized);
    try {
      const saved = JSON.parse(localStorage.getItem('zoomies.eli.layout') ?? '{}');
      if (['compact', 'comfortable', 'expanded'].includes(saved.size)) size = saved.size;
      if (['left', 'right'].includes(saved.side)) side = saved.side;
    } catch {
      /* Storage is optional. */
    }
    return () => {
      media.removeEventListener('change', resized);
      request?.abort();
      eli.reset();
      eli.open = false;
    };
  });
  $effect(() => {
    if (!eli.open || !panel) return;
    const layer = layers.push('dialog', () => eli.close());
    const unlock = modal ? lockScroll() : () => {};
    const uninert = modal ? pageInert(panel) : () => {};
    const trap = modal ? trapFocus(panel) : undefined;
    return () => {
      layers.remove(layer);
      uninert();
      unlock();
      trap?.destroy();
    };
  });
  function remember(): void {
    try {
      localStorage.setItem('zoomies.eli.layout', JSON.stringify({ size, side }));
    } catch {
      /* Storage is optional. */
    }
  }
  async function load(): Promise<void> {
    request?.abort();
    const controller = new AbortController();
    request = controller;
    loading = true;
    error = '';
    try {
      const result = await listAssistantProviders(controller.signal);
      if (!controller.signal.aborted) eli.know(result.items ?? []);
    } catch (cause) {
      if (!controller.signal.aborted)
        error =
          cause instanceof Error ? cause.message : 'Eli could not load its provider. Try again.';
    } finally {
      if (request === controller) loading = false;
    }
  }
  $effect(() => {
    if (!eli.open) return;
    void load();
    void tick().then(() => panel?.focus());
  });
  $effect(() => {
    if (!eli.open || loading || !answering || !panel) return;
    void tick().then(() => {
      if (eli.open && panel?.contains(document.activeElement))
        panel.querySelector<HTMLTextAreaElement>('textarea[aria-label="Message"]')?.focus();
    });
  });
  $effect(() => {
    if (!eli.open || loading || !answering || eli.conversation.busy || !eli.pending.length) return;
    const prompt = eli.pending.shift();
    if (prompt) void eli.conversation.send(contextPrompt(prompt), prompt, answering.id);
  });
  $effect(() => {
    void size;
    void side;
    void full;
    if (panel) {
      panel.style.width = '';
      panel.style.height = '';
    }
  });
  function launch(): void {
    eli.returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    eli.open = true;
  }
</script>

{#if !eli.open}
  <!-- Exempt from the inert that a drawer or dialog puts on the rest of the
       page: Eli stays clickable above whatever is open. -->
  <div class="launcher" data-side={side} data-inert-exempt>
    <button
      class="launch"
      onclick={launch}
      aria-label="Ask Eli"
      title="Ask Eli"
      aria-expanded="false"
      aria-controls="eli-widget"><EliAvatar size={56} /></button
    >
  </div>
{:else}
  <div
    id="eli-widget"
    class="panel"
    data-size={size}
    data-side={side}
    class:full
    data-inert-exempt
    role="dialog"
    aria-modal={modal}
    aria-label="Eli assistant"
    tabindex="-1"
    bind:this={panel}
    use:movable={!modal}
    onkeydown={(event) => {
      if (event.key === 'Escape' && layers.top()?.kind === 'dialog') {
        event.stopPropagation();
        eli.close();
      }
    }}
  >
    <header>
      <button
        type="button"
        class="identity"
        data-drag-handle
        disabled={modal}
        aria-label="Move Eli panel using arrow keys or drag"
        title="Drag me around, or use arrow keys"
      >
        <EliAvatar size={32} />
        <div>
          <span class="name">Eli</span>
          <span class="subtitle">
            {#if answering}Answers from <strong>{answering.name}</strong>, {answering.model}{:else}Your
              fleet companion. All ears.{/if}
          </span>
        </div>
      </button>
      <div class="tools">
        <IconButton
          icon={SquarePen}
          label="New conversation"
          disabled={!eli.conversation.turns.length && !eli.pending.length}
          onclick={() => eli.newConversation()}
        />
        <IconButton
          icon={full ? Minimize2 : Maximize2}
          label={full ? 'Restore panel' : 'Full screen'}
          pressed={full}
          onclick={() => {
            full = !full;
          }}
        />
        <IconButton icon={X} label="Minimise Eli" onclick={() => eli.close()} />
      </div>
    </header>
    <div class="layout">
      <div class="sizes" role="group" aria-label="Eli panel size">
        {#each ['compact', 'comfortable', 'expanded'] as preset (preset)}
          <button
            type="button"
            aria-pressed={size === preset}
            disabled={full}
            onclick={() => {
              size = preset;
              remember();
            }}
            >{preset === 'comfortable'
              ? 'Default'
              : preset === 'compact'
                ? 'Compact'
                : 'Expanded'}</button
          >
        {/each}
      </div>
      <div class="position" role="group" aria-label="Panel position">
        <IconButton
          icon={PanelLeft}
          label="Place Eli on the left"
          pressed={side === 'left'}
          disabled={full}
          onclick={() => {
            side = 'left';
            panel?.dispatchEvent(new Event('eli-place'));
            remember();
          }}
        />
        <IconButton
          icon={PanelRight}
          label="Place Eli on the right"
          pressed={side === 'right'}
          disabled={full}
          onclick={() => {
            side = 'right';
            panel?.dispatchEvent(new Event('eli-place'));
            remember();
          }}
        />
      </div>
    </div>
    <div class="content">
      {#if loading}<p class="notice" role="status">Connecting to Eli…</p>
      {:else if error}<div class="notice" role="alert">
          {error}
          <Button size="sm" onclick={() => void load()}>Try again</Button>
        </div>
      {:else if !answering}<p class="notice">
          Choose an enabled default provider in <a
            href="/settings/assistant"
            onclick={() => eli.close()}>Eli AI Assistant settings</a
          > to start chatting.
        </p>{/if}
      {#if answering && !answering.fleet_access}
        <p class="notice">
          Eli cannot see this fleet through {answering.name}. Use Ask Eli to share displayed
          details, or enable read-only fleet access in
          <a
            href="/settings/assistant"
            onclick={(event) => {
              event.preventDefault();
              eli.close();
              router.navigate('/settings/assistant');
            }}>Settings, Eli AI Assistant</a
          >.
        </p>
      {/if}
      {#if eli.pending.length}
        <details class="pending">
          <summary
            >{eli.pending.length} contextual {eli.pending.length === 1 ? 'question' : 'questions'} waiting{eli
              .conversation.busy
              ? ' for the current answer'
              : ''}</summary
          >
          {#each eli.pending as prompt, i (i)}<p>{prompt.title}</p>{/each}
          <Button
            size="sm"
            variant="ghost"
            onclick={() => {
              eli.pending = [];
            }}>Discard waiting questions</Button
          >
        </details>
      {/if}
      <ConversationView
        conversation={eli.conversation}
        fleetAccess={answering?.fleet_access ?? false}
        {answering}
        height="100%"
        closedHint={loading
          ? 'Connecting to Eli'
          : error || 'Set up a default provider in Eli AI Assistant settings'}
      />
    </div>
    <footer>
      {answering ? `${answering.name} · ${answering.model}` : 'Eli'}<span
        >Context shared on request</span
      >
    </footer>
  </div>
{/if}

<style>
  .launch {
    display: flex;
    width: var(--z-eli-launcher);
    height: var(--z-eli-launcher);
    padding: 0;
    border: 0;
    border-radius: var(--z-radius-full);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-lg);
    cursor: pointer;
  }
  .launcher {
    position: fixed;
    right: var(--z-space-5);
    bottom: var(--z-space-10);
    z-index: var(--z-layer-assistant);
  }
  .launcher[data-side='left'] {
    right: auto;
    left: var(--z-space-5);
  }
  .panel {
    box-sizing: border-box;
    position: fixed;
    right: var(--z-space-4);
    bottom: var(--z-space-10);
    z-index: var(--z-layer-assistant);
    display: flex;
    flex-direction: column;
    width: min(28rem, calc(var(--z-window-width) - var(--z-space-8)));
    height: min(42rem, calc(100dvh - var(--z-space-16)));
    min-width: min(22rem, calc(var(--z-window-width) - var(--z-space-8)));
    min-height: min(26rem, calc(100dvh - var(--z-space-16)));
    max-width: calc(var(--z-window-width) - var(--z-space-8));
    max-height: calc(100dvh - var(--z-space-16));
    background: var(--z-surface-raised);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-lg);
    box-shadow: var(--z-shadow-lg);
    overflow: hidden;
    resize: both;
  }
  .panel[data-side='left'] {
    right: auto;
    left: var(--z-space-4);
  }
  .panel[data-size='compact'] {
    width: min(24rem, calc(var(--z-window-width) - var(--z-space-8)));
    height: min(32rem, calc(100dvh - var(--z-space-16)));
  }
  .panel[data-size='expanded'] {
    width: min(46rem, calc(var(--z-window-width) - var(--z-space-8)));
    height: min(52rem, calc(100dvh - var(--z-space-16)));
  }
  .panel.full {
    left: var(--z-space-4);
    right: auto;
    bottom: var(--z-space-4);
    width: calc(var(--z-window-width) - var(--z-space-8));
    height: calc(100dvh - var(--z-space-8));
    max-height: calc(100dvh - var(--z-space-8));
    resize: none;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
  }
  .identity,
  .tools,
  .position {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  .name,
  p {
    margin: 0;
  }
  .name {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .identity {
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: grab;
    touch-action: none;
    user-select: none;
  }
  .identity:active {
    cursor: grabbing;
  }
  .identity:disabled {
    cursor: default;
  }
  .name,
  .subtitle {
    display: block;
  }
  .identity > div {
    min-width: 0;
  }
  .identity .subtitle {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tools {
    flex-shrink: 0;
  }
  .identity .subtitle,
  footer {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .layout {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--z-space-1) var(--z-space-4);
    border-block: var(--z-border-width) solid var(--z-border);
  }
  .sizes {
    display: flex;
    gap: var(--z-space-1);
  }
  .sizes button {
    padding: var(--z-space-2);
    font: inherit;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    background: transparent;
    border: var(--z-border-width) solid transparent;
    border-radius: var(--z-radius-md);
    cursor: pointer;
  }
  .sizes button[aria-pressed='true'] {
    color: var(--z-accent);
    background: var(--z-accent-subtle);
    border-color: var(--z-accent-border);
  }
  .sizes button:hover:not(:disabled) {
    background: var(--z-surface-hover);
  }
  .sizes button:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .content {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-height: 0;
    padding: var(--z-space-3);
    gap: var(--z-space-2);
  }
  .content :global(.view) {
    flex: 1;
  }
  .notice,
  .pending {
    flex: none;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .pending {
    max-height: 8rem;
    overflow: auto;
    padding: var(--z-space-2);
    background: var(--z-surface-sunken);
    border-radius: var(--z-radius-md);
  }
  .pending p {
    margin-block: var(--z-space-2);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  summary {
    cursor: pointer;
  }
  footer {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: var(--z-space-2);
    padding: 0 var(--z-space-4) var(--z-space-2);
  }
  @media (max-width: 767px) {
    .launcher {
      bottom: calc(var(--z-space-12) + var(--z-space-4) + var(--z-safe-bottom));
    }
    header {
      padding-top: calc(var(--z-space-3) + var(--z-safe-top));
    }
    footer {
      padding-bottom: var(--z-safe-bottom);
    }
    .panel,
    .panel[data-size],
    .panel[data-side],
    .panel.full {
      left: 0;
      right: auto;
      bottom: 0;
      width: var(--z-window-width);
      min-width: 0;
      max-width: var(--z-window-width);
      height: 100dvh;
      max-height: 100dvh;
      border-radius: 0;
      resize: none;
    }
    .layout {
      display: none;
    }
  }
</style>
