<!--
  The state filter: a checkbox for every runner state, because an operator
  usually wants two or three of them at once ("show me busy and draining").

  It behaves like the column chooser in the grid -- a popover registered on the
  layer stack so Escape closes exactly this and nothing else, and a click
  outside dismisses it.
-->
<script lang="ts">
  import { ListFilter } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import { layers } from '$lib/keys';
  import { runnerStatuses } from '$lib/status';

  interface Props {
    /** The states currently filtered on, as the API spells them. */
    selected: readonly string[];
    onchange: (states: string[]) => void;
    class?: string;
  }

  let { selected, onchange, class: className = '' }: Props = $props();

  const states = runnerStatuses();
  const menuId = $props.id();

  let open = $state(false);
  let container = $state<HTMLDivElement | null>(null);
  let panel = $state<HTMLDivElement | null>(null);

  $effect(() => {
    if (!open) return;
    const layer = layers.push('dropdown', () => (open = false));
    const onPointer = (event: MouseEvent) => {
      if (!container?.contains(event.target as Node)) open = false;
    };
    document.addEventListener('mousedown', onPointer);
    return () => {
      layers.remove(layer);
      document.removeEventListener('mousedown', onPointer);
    };
  });

  // Opening a menu with the keyboard should land inside it.
  $effect(() => {
    if (!open || !panel) return;
    panel.querySelector<HTMLInputElement>('input')?.focus();
  });

  function toggle(key: string, on: boolean): void {
    const next = on ? [...selected, key] : selected.filter((s) => s !== key);
    onchange([...new Set(next)]);
  }

  const label = $derived(
    selected.length === 0
      ? 'Any state'
      : selected.length === 1
        ? (states.find((s) => s.key === selected[0])?.label ?? 'State')
        : `${selected.length} states`,
  );
</script>

<div class="wrap {className}" bind:this={container}>
  <Button
    size="sm"
    variant="secondary"
    icon={ListFilter}
    ariaExpanded={open}
    ariaHaspopup="true"
    ariaControls={menuId}
    onclick={() => (open = !open)}
  >
    {label}
  </Button>
  {#if open}
    <div
      class="menu"
      id={menuId}
      role="group"
      aria-label="Filter by runner state"
      bind:this={panel}
    >
      {#each states as state (state.key)}
        <div class="row">
          <Checkbox
            checked={selected.includes(state.key)}
            onchange={(on) => toggle(state.key, on)}
            ariaLabel={state.label}
          />
          <StatusDot status={state} size="sm" />
          <span class="name">{state.label}</span>
        </div>
      {/each}
      {#if selected.length > 0}
        <button type="button" class="reset" onclick={() => onchange([])}>Clear states</button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .wrap {
    position: relative;
  }
  .menu {
    position: absolute;
    left: 0;
    top: calc(100% + var(--z-space-1));
    z-index: var(--z-layer-dropdown);
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    min-width: 12rem;
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-md);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  .name {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .reset {
    margin-top: var(--z-space-1);
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--z-accent);
    font-family: inherit;
    font-size: var(--z-text-xs);
    text-align: left;
    text-decoration: underline;
    cursor: pointer;
  }
</style>
