<!--
  A multi-select filter menu.

  The repository, workflow, pool, label and outcome filters are all the same
  object: a trigger that says how many values are chosen, and a panel of
  checkboxes. Once the list is long enough to scroll, a type-to-narrow box
  appears above it, because a fleet that has run jobs for two hundred
  repositories should not make an operator hunt.

  It is a non-modal popup: Tab moves through the checkboxes, Escape closes it
  through the shared layer stack, and focus returns to the trigger.
-->
<script lang="ts">
  import { ChevronDown } from '@lucide/svelte';
  import { layers } from '$lib/keys';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Input from '$lib/components/Input.svelte';

  export interface FacetOption {
    value: string;
    label: string;
    /** A second line under the label: a pool's target, a state's meaning. */
    hint?: string;
  }

  interface Props {
    /** The field being filtered: "Repository". */
    label: string;
    options: readonly FacetOption[];
    selected: readonly string[];
    onchange: (next: string[]) => void;
    /** Show the type-to-narrow box once there are more options than this. */
    searchAfter?: number;
    /** What to say when there is nothing to choose from yet. */
    emptyHint?: string;
    disabled?: boolean;
    class?: string;
  }

  let {
    label,
    options,
    selected,
    onchange,
    searchAfter = 8,
    emptyHint = 'Nothing to filter by yet.',
    disabled = false,
    class: className = '',
  }: Props = $props();

  const uid = $props.id();
  const panelId = `facet-${uid}`;

  let open = $state(false);
  let query = $state('');
  let wrap = $state<HTMLDivElement | null>(null);
  let trigger = $state<HTMLDivElement | null>(null);

  const shown = $derived(
    query.trim()
      ? options.filter((o) => o.label.toLowerCase().includes(query.trim().toLowerCase()))
      : options,
  );

  function close(restoreFocus = true): void {
    if (!open) return;
    open = false;
    query = '';
    if (restoreFocus) trigger?.querySelector('button')?.focus();
  }

  function toggle(value: string, on: boolean): void {
    onchange(on ? [...selected, value] : selected.filter((v) => v !== value));
  }

  $effect(() => {
    if (!open) return;
    const layer = layers.push('dropdown', () => close());
    const onDocument = (event: MouseEvent) => {
      if (!wrap?.contains(event.target as Node)) close(false);
    };
    document.addEventListener('mousedown', onDocument);
    return () => {
      layers.remove(layer);
      document.removeEventListener('mousedown', onDocument);
    };
  });
</script>

<div class="facet {className}" bind:this={wrap}>
  <div bind:this={trigger}>
    <Button
      size="sm"
      variant={selected.length > 0 ? 'secondary' : 'ghost'}
      iconAfter={ChevronDown}
      {disabled}
      ariaExpanded={open}
      ariaControls={panelId}
      ariaHaspopup="dialog"
      onclick={() => (open ? close() : (open = true))}
    >
      {label}{selected.length > 0 ? ` · ${selected.length}` : ''}
    </Button>
  </div>

  {#if open}
    <div class="panel" id={panelId} role="group" aria-label="Filter by {label.toLowerCase()}">
      {#if options.length === 0}
        <p class="empty">{emptyHint}</p>
      {:else}
        {#if options.length > searchAfter}
          <div class="search">
            <Input
              bind:value={query}
              type="search"
              size="sm"
              placeholder="Narrow this list"
              ariaLabel="Narrow the {label.toLowerCase()} list"
            />
          </div>
        {/if}
        <div class="list">
          {#each shown as option (option.value)}
            <Checkbox
              label={option.label}
              description={option.hint}
              checked={selected.includes(option.value)}
              onchange={(on) => toggle(option.value, on)}
            />
          {/each}
          {#if shown.length === 0}
            <p class="empty">Nothing matches “{query}”.</p>
          {/if}
        </div>
        {#if selected.length > 0}
          <div class="foot">
            <Button size="sm" variant="ghost" onclick={() => onchange([])}>Clear</Button>
          </div>
        {/if}
      {/if}
    </div>
  {/if}
</div>

<style>
  .facet {
    position: relative;
  }
  .panel {
    position: absolute;
    left: 0;
    top: calc(100% + var(--z-space-1));
    z-index: var(--z-layer-dropdown);
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    min-width: 240px;
    max-width: 340px;
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-md);
  }
  .search {
    flex: none;
  }
  .list {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    max-height: 260px;
    overflow-y: auto;
  }
  .empty {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .foot {
    display: flex;
    justify-content: flex-end;
    border-top: 1px solid var(--z-border);
    padding-top: var(--z-space-2);
  }
</style>
