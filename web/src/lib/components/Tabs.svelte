<!--
  Tabs with the roles and arrow-key behaviour the pattern requires. The panel is
  rendered by the caller through the `children` snippet, which receives the
  active tab id.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { LucideIcon } from '@lucide/svelte';

  export interface TabItem {
    id: string;
    label: string;
    icon?: LucideIcon;
    /** A count beside the label: unread problems, live runners. */
    badge?: string | number;
    disabled?: boolean;
  }

  interface Props {
    tabs: readonly TabItem[];
    value?: string;
    /** Names the tab list for assistive technology: "Runner detail sections". */
    label: string;
    onchange?: (id: string) => void;
    class?: string;
    children?: Snippet<[string]>;
  }

  let {
    tabs,
    value = $bindable(tabs[0]?.id ?? ''),
    label,
    onchange,
    class: className = '',
    children,
  }: Props = $props();

  const uid = $props.id();
  const id = `tabs-${uid}`;
  let list = $state<HTMLDivElement | null>(null);

  function select(next: string): void {
    if (next === value) return;
    value = next;
    onchange?.(next);
  }

  function move(delta: number): void {
    const index = tabs.findIndex((t) => t.id === value);
    let next = index;
    for (let i = 0; i < tabs.length; i++) {
      next = (next + delta + tabs.length) % tabs.length;
      if (!tabs[next]?.disabled) break;
    }
    const tab = tabs[next];
    if (!tab) return;
    select(tab.id);
    queueMicrotask(() => list?.querySelector<HTMLElement>(`#${id}-tab-${cssId(tab.id)}`)?.focus());
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'ArrowRight') {
      event.preventDefault();
      move(1);
    } else if (event.key === 'ArrowLeft') {
      event.preventDefault();
      move(-1);
    } else if (event.key === 'Home') {
      event.preventDefault();
      const first = tabs.find((t) => !t.disabled);
      if (first) select(first.id);
    } else if (event.key === 'End') {
      event.preventDefault();
      const last = [...tabs].reverse().find((t) => !t.disabled);
      if (last) select(last.id);
    }
  }

  /** Tab ids come from the API, so make them safe for a CSS selector. */
  function cssId(raw: string): string {
    return raw.replace(/[^a-zA-Z0-9_-]/g, '-');
  }
</script>

<div class="tabs {className}">
  <div bind:this={list} class="list" role="tablist" aria-label={label}>
    {#each tabs as tab (tab.id)}
      <button
        type="button"
        role="tab"
        id="{id}-tab-{cssId(tab.id)}"
        class="tab"
        aria-selected={value === tab.id}
        aria-controls="{id}-panel"
        tabindex={value === tab.id ? 0 : -1}
        disabled={tab.disabled}
        onclick={() => select(tab.id)}
        onkeydown={onKeydown}
      >
        {#if tab.icon}
          {@const TabIcon = tab.icon}
          <TabIcon size={14} aria-hidden="true" />
        {/if}
        <span>{tab.label}</span>
        {#if tab.badge !== undefined}<span class="badge tabular">{tab.badge}</span>{/if}
      </button>
    {/each}
  </div>
  {#if children}
    <div id="{id}-panel" role="tabpanel" aria-labelledby="{id}-tab-{cssId(value)}" tabindex="0">
      {@render children(value)}
    </div>
  {/if}
</div>

<style>
  .list {
    display: flex;
    align-items: center;
    gap: var(--z-space-1);
    border-bottom: 1px solid var(--z-border);
    overflow-x: auto;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    padding: var(--z-space-2) var(--z-space-3);
    border: 0;
    border-bottom: 2px solid transparent;
    background: transparent;
    color: var(--z-text-muted);
    font-family: inherit;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    white-space: nowrap;
    cursor: pointer;
    transition: color var(--z-motion-fast) var(--z-ease);
  }
  .tab:hover:not(:disabled) {
    color: var(--z-text);
  }
  .tab[aria-selected='true'] {
    color: var(--z-accent);
    border-bottom-color: var(--z-accent);
  }
  .tab:disabled {
    color: var(--z-text-subtle);
    cursor: not-allowed;
  }
  .badge {
    padding: 0 var(--z-space-1);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-variant-numeric: tabular-nums;
  }
  /* The panel is in the tab order, so it keeps the shared focus ring: a
     keyboard user tabbing out of the tab list must be able to see where they
     landed. Mouse focus is already ringless through :focus-visible. */
  [role='tabpanel'] {
    padding-top: var(--z-space-4);
  }
</style>
