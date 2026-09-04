<script lang="ts">
  import type { LucideIcon } from '@lucide/svelte';
  import type { HTMLInputAttributes } from 'svelte/elements';

  interface Props {
    value?: string;
    id?: string;
    name?: string;
    type?: 'text' | 'password' | 'email' | 'number' | 'search' | 'url';
    placeholder?: string;
    disabled?: boolean;
    readonly?: boolean;
    required?: boolean;
    invalid?: boolean;
    describedBy?: string;
    autocomplete?: HTMLInputAttributes['autocomplete'];
    ariaLabel?: string;
    /** IDs, labels and durations are monospaced. */
    mono?: boolean;
    size?: 'sm' | 'md';
    icon?: LucideIcon;
    min?: number;
    max?: number;
    step?: number;
    element?: HTMLInputElement | null;
    oninput?: (event: Event) => void;
    onchange?: (event: Event) => void;
    onblur?: (event: FocusEvent) => void;
    onkeydown?: (event: KeyboardEvent) => void;
    class?: string;
  }

  let {
    value = $bindable(''),
    id,
    name,
    type = 'text',
    placeholder,
    disabled = false,
    readonly = false,
    required = false,
    invalid = false,
    describedBy,
    autocomplete,
    ariaLabel,
    mono = false,
    size = 'md',
    icon: Icon,
    min,
    max,
    step,
    element = $bindable(null),
    oninput,
    onchange,
    onblur,
    onkeydown,
    class: className = '',
  }: Props = $props();

  /*
    The value handed back is always the string the user typed, for every
    `type`. Svelte's `bind:value` would have coerced it to a number on a
    number input, and because `type` is a prop the caller cannot see that
    coming: a form that stores strings would silently start holding numbers,
    and the first `.trim()` downstream throws. Binding by hand keeps this
    component's declared contract -- value is a string -- true of every type
    it accepts.
  */
  function handleInput(event: Event): void {
    value = (event.currentTarget as HTMLInputElement).value;
    oninput?.(event);
  }
</script>

<div class="wrap {size} {className}" class:has-icon={Boolean(Icon)}>
  {#if Icon}
    <span class="icon" aria-hidden="true"><Icon size={14} /></span>
  {/if}
  <input
    bind:this={element}
    {value}
    {id}
    {name}
    {type}
    {placeholder}
    {disabled}
    {readonly}
    {required}
    {autocomplete}
    {min}
    {max}
    {step}
    class:mono
    aria-label={ariaLabel}
    aria-invalid={invalid ? 'true' : undefined}
    aria-describedby={describedBy}
    oninput={handleInput}
    {onchange}
    {onblur}
    {onkeydown}
  />
</div>

<style>
  .wrap {
    position: relative;
    display: flex;
    align-items: center;
  }
  .icon {
    position: absolute;
    left: var(--z-space-2);
    display: inline-flex;
    color: var(--z-text-subtle);
    pointer-events: none;
  }
  input {
    width: 100%;
    font-family: var(--z-font-sans);
    font-size: var(--z-text-base);
    color: var(--z-text);
    background: var(--z-surface);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    transition:
      border-color var(--z-motion-fast) var(--z-ease),
      background-color var(--z-motion-fast) var(--z-ease);
  }
  .md input {
    height: var(--z-space-8);
    padding: 0 var(--z-space-3);
  }
  .sm input {
    height: var(--z-space-6);
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-sm);
  }
  .has-icon.md input {
    padding-left: var(--z-space-8);
  }
  .has-icon.sm input {
    padding-left: var(--z-space-6);
  }
  input.mono {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-sm);
  }
  input::placeholder {
    color: var(--z-text-subtle);
  }
  input:hover:not(:disabled) {
    border-color: var(--z-text-subtle);
  }
  input:disabled {
    background: var(--z-surface-sunken);
    color: var(--z-text-subtle);
    cursor: not-allowed;
  }
  input[aria-invalid='true'] {
    border-color: var(--z-danger);
  }
  input[type='search']::-webkit-search-cancel-button {
    appearance: none;
  }
</style>
