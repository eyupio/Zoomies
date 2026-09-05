<!--
  Wraps any identifier. Announces "copied" once, politely, then goes quiet.
-->
<script lang="ts">
  import { Check, Copy } from '@lucide/svelte';

  interface Props {
    value: string;
    /** What is being copied, for the accessible name: "Copy runner ID". */
    label?: string;
    size?: 'sm' | 'md';
    /** Show the value next to the button, monospaced. */
    showValue?: boolean;
    /**
     * Print the label on the button as well as naming it. The icon alone is
     * right beside an ID in a dense row; where copying is the whole point of
     * the screen -- a command to paste on another machine -- it needs words.
     */
    showLabel?: boolean;
    class?: string;
  }

  let {
    value,
    label = 'Copy',
    size = 'sm',
    showValue = false,
    showLabel = false,
    class: className = '',
  }: Props = $props();

  let copied = $state(false);
  let failed = $state(false);
  let timer: ReturnType<typeof setTimeout> | null = null;

  async function copy(): Promise<void> {
    if (timer) clearTimeout(timer);
    try {
      await navigator.clipboard.writeText(value);
      copied = true;
      failed = false;
    } catch {
      // Clipboard access is refused outside a secure context; say so rather
      // than silently doing nothing.
      failed = true;
      copied = false;
    }
    timer = setTimeout(() => {
      copied = false;
      failed = false;
    }, 2000);
  }
</script>

<span class="copy {className}">
  {#if showValue}<code class="value">{value}</code>{/if}
  <button
    type="button"
    class="btn {size}"
    class:labelled={showLabel}
    aria-label={copied ? `${label}: copied` : label}
    title={label}
    onclick={copy}
  >
    {#if copied}
      <Check size={size === 'sm' ? 12 : 14} aria-hidden="true" />
    {:else}
      <Copy size={size === 'sm' ? 12 : 14} aria-hidden="true" />
    {/if}
    {#if showLabel}<span class="text">{copied ? 'Copied' : label}</span>{/if}
  </button>
  <span class="sr-only" aria-live="polite">
    {#if copied}Copied to the clipboard{:else if failed}Copying needs a secure connection{/if}
  </span>
</span>

<style>
  .copy {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    max-width: 100%;
  }
  .value {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 0;
    border-radius: var(--z-radius-sm);
    background: transparent;
    color: var(--z-text-subtle);
    cursor: pointer;
    transition: color var(--z-motion-fast) var(--z-ease);
  }
  .sm {
    width: var(--z-space-5);
    height: var(--z-space-5);
  }
  .md {
    width: var(--z-space-6);
    height: var(--z-space-6);
  }
  .btn:hover {
    color: var(--z-text);
    background: var(--z-surface-hover);
  }
  .labelled {
    width: auto;
    height: auto;
    gap: var(--z-space-2);
    padding: var(--z-space-2) var(--z-space-3);
    border: 1px solid var(--z-border-strong);
    background: var(--z-surface);
    color: var(--z-text);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    white-space: nowrap;
  }
  .labelled.sm {
    padding: var(--z-space-1) var(--z-space-2);
    font-size: var(--z-text-xs);
  }
</style>
