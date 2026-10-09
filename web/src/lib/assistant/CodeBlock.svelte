<!--
  A fenced block of code from an answer: monospaced, scrollable sideways and
  never wrapped, with the language it says it is in and a button to copy it.
-->
<script lang="ts">
  import CopyButton from '$lib/components/CopyButton.svelte';

  let { code, lang = '' }: { code: string; lang?: string } = $props();
</script>

<figure>
  <figcaption>
    <span>{lang || 'text'}</span>
    <CopyButton value={code} label="Copy code" />
  </figcaption>
  <!-- A region that scrolls must be reachable by keyboard, or its far end cannot be read. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <pre tabindex="0"><code>{code}</code></pre>
</figure>

<style>
  figure {
    margin: 0;
    min-width: 0;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
    overflow: hidden;
  }
  figcaption {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2);
    padding: var(--z-space-1) var(--z-space-2) var(--z-space-1) var(--z-space-3);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  pre {
    margin: 0;
    padding: var(--z-space-3);
    overflow-x: auto;
  }
  code {
    padding: 0;
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-sm);
    white-space: pre;
    background: none;
    border: 0;
  }
</style>
