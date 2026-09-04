<!--
  A chip input for runner labels.

  Enter or a comma commits the label; Backspace on an empty box takes the last
  one back; every chip has its own remove button, so the control is fully
  operable from the keyboard without any custom roving-tabindex machinery.
-->
<script lang="ts">
  import { X } from '@lucide/svelte';
  // The label vocabulary lives in $lib/brand, the browser's copy of
  // internal/store/brand.go.
  import { isImplicit } from '$lib/brand';

  interface Props {
    value?: string[];
    id?: string;
    describedBy?: string;
    invalid?: boolean;
    disabled?: boolean;
    placeholder?: string;
    /** Fired when the box loses focus, so a form can mark the field touched. */
    onblur?: () => void;
    class?: string;
  }

  let {
    value = $bindable([]),
    id,
    describedBy,
    invalid = false,
    disabled = false,
    placeholder = 'Type a label, then press Enter',
    onblur,
    class: className = '',
  }: Props = $props();

  let draft = $state('');
  let announcement = $state('');
  let element = $state<HTMLInputElement | null>(null);

  function commit(raw: string): void {
    const label = raw.trim();
    if (label === '') return;
    if (value.some((existing) => existing.toLowerCase() === label.toLowerCase())) {
      announcement = `${label} is already on this pool`;
      draft = '';
      return;
    }
    value = [...value, label];
    announcement = `Added ${label}`;
    draft = '';
  }

  function remove(label: string): void {
    value = value.filter((existing) => existing !== label);
    announcement = `Removed ${label}`;
    element?.focus();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter' || event.key === ',') {
      event.preventDefault();
      commit(draft);
      return;
    }
    if (event.key === 'Backspace' && draft === '' && value.length > 0) {
      event.preventDefault();
      const last = value[value.length - 1];
      if (last !== undefined) remove(last);
    }
  }

  function onPaste(event: ClipboardEvent): void {
    const text = event.clipboardData?.getData('text') ?? '';
    if (!text.includes(',') && !text.includes('\n')) return;
    event.preventDefault();
    for (const part of text.split(/[,\n]/)) commit(part);
  }
</script>

<div class="box {className}" class:invalid class:disabled>
  {#if value.length > 0}
    <ul class="chips">
      {#each value as label (label)}
        <li>
          <span class="chip" class:implicit={isImplicit(label)}>
            <span class="text">{label}</span>
            <button
              type="button"
              {disabled}
              aria-label="Remove the label {label}"
              onclick={() => remove(label)}
            >
              <X size={11} aria-hidden="true" />
            </button>
          </span>
        </li>
      {/each}
    </ul>
  {/if}
  <input
    bind:this={element}
    bind:value={draft}
    {id}
    {disabled}
    {placeholder}
    type="text"
    autocomplete="off"
    spellcheck="false"
    aria-invalid={invalid ? 'true' : undefined}
    aria-describedby={describedBy}
    onkeydown={onKeydown}
    onblur={() => {
      commit(draft);
      onblur?.();
    }}
    onpaste={onPaste}
  />
</div>
<p class="sr-only" aria-live="polite">{announcement}</p>

<style>
  .box {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    min-height: var(--z-space-8);
    padding: var(--z-space-1) var(--z-space-2);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
  }
  .box:focus-within {
    border-color: var(--z-accent);
  }
  .box.invalid {
    border-color: var(--z-danger);
  }
  .box.disabled {
    opacity: 0.6;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    padding: 0 var(--z-space-1) 0 var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .chip.implicit {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
    color: var(--z-pending);
  }
  .chip button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: 0;
    border-radius: var(--z-radius-sm);
    background: transparent;
    color: inherit;
    cursor: pointer;
    opacity: 0.7;
  }
  .chip button:hover {
    opacity: 1;
    color: var(--z-danger);
  }
  input {
    flex: 1 1 12ch;
    min-width: 12ch;
    border: 0;
    padding: var(--z-space-1) 0;
    background: transparent;
    color: var(--z-text);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-base);
  }
  input:focus {
    outline: none;
  }
  input::placeholder {
    font-family: var(--z-font-sans);
    color: var(--z-text-subtle);
  }
</style>
