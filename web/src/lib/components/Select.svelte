<script lang="ts">
  import { ChevronDown } from '@lucide/svelte';

  export interface SelectOption {
    value: string;
    label: string;
    disabled?: boolean;
  }

  interface Props {
    value?: string;
    options: readonly SelectOption[];
    id?: string;
    name?: string;
    /** Rendered as a disabled first option, for "no filter chosen yet". */
    placeholder?: string;
    disabled?: boolean;
    required?: boolean;
    invalid?: boolean;
    describedBy?: string;
    ariaLabel?: string;
    size?: 'sm' | 'md';
    onchange?: (value: string) => void;
    class?: string;
  }

  let {
    value = $bindable(''),
    options,
    id,
    name,
    placeholder,
    disabled = false,
    required = false,
    invalid = false,
    describedBy,
    ariaLabel,
    size = 'md',
    onchange,
    class: className = '',
  }: Props = $props();

  function handle(event: Event): void {
    const target = event.currentTarget as HTMLSelectElement;
    onchange?.(target.value);
  }
</script>

<div class="wrap {size} {className}">
  <select
    bind:value
    {id}
    {name}
    {disabled}
    {required}
    aria-label={ariaLabel}
    aria-invalid={invalid ? 'true' : undefined}
    aria-describedby={describedBy}
    onchange={handle}
  >
    {#if placeholder}
      <option value="" disabled={required}>{placeholder}</option>
    {/if}
    {#each options as option (option.value)}
      <option value={option.value} disabled={option.disabled}>{option.label}</option>
    {/each}
  </select>
  <ChevronDown class="chev" size={14} aria-hidden="true" />
</div>

<style>
  .wrap {
    position: relative;
    display: inline-flex;
    align-items: center;
    width: 100%;
  }
  select {
    appearance: none;
    width: 100%;
    font-family: var(--z-font-sans);
    font-size: var(--z-text-base);
    color: var(--z-text);
    background: var(--z-surface);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    cursor: pointer;
  }
  .md select {
    height: var(--z-space-8);
    padding: 0 var(--z-space-8) 0 var(--z-space-3);
  }
  .sm select {
    height: var(--z-space-6);
    padding: 0 var(--z-space-6) 0 var(--z-space-2);
    font-size: var(--z-text-sm);
  }
  select:disabled {
    background: var(--z-surface-sunken);
    color: var(--z-text-subtle);
    cursor: not-allowed;
  }
  select[aria-invalid='true'] {
    border-color: var(--z-danger);
  }
  .wrap :global(.chev) {
    position: absolute;
    right: var(--z-space-2);
    color: var(--z-text-subtle);
    pointer-events: none;
  }

  /*
    16px on a phone, and only on a phone.

    The base control size is 14px, which is right for a dense operator UI on a
    desktop -- but mobile Safari zooms the whole viewport whenever a focused
    control's font-size is under 16px, and the viewport meta deliberately does
    not set maximum-scale. So every field tap on the first-run screens jumped
    the 360px page to roughly 410px effective width and ran the card off both
    edges, once per field. Height comes from the space scale, so nothing
    reflows; only the glyphs grow.
  */
  @media (max-width: 768px) {
    select {
      font-size: var(--z-text-lg);
    }
  }
</style>
