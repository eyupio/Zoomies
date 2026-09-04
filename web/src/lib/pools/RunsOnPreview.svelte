<!--
  The workflow snippet these labels produce.

  Pool labels only mean something in relation to a `runs-on:` line, so the
  wizard shows that line as it is typed rather than describing it. `self-hosted`
  is added because GitHub applies it to every self-hosted runner.
-->
<script lang="ts">
  import CopyButton from '$lib/components/CopyButton.svelte';

  interface Props {
    labels?: readonly string[];
    class?: string;
  }

  let { labels = [], class: className = '' }: Props = $props();

  const list = $derived(['self-hosted', ...labels].join(', '));
  const line = $derived(`runs-on: [${list}]`);
  const snippet = $derived(`jobs:\n  build:\n    ${line}`);
</script>

<figure class="preview {className}">
  <figcaption>
    <span>What a workflow writes</span>
    <CopyButton value={snippet} label="Copy the runs-on snippet" />
  </figcaption>
  <pre><code>{snippet}</code></pre>
  {#if labels.length === 0}
    <p class="hint">
      With no labels of its own this pool answers every self-hosted job, which is rarely what an
      operator wants.
    </p>
  {/if}
</figure>

<style>
  .preview {
    margin: 0;
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
    overflow: hidden;
  }
  figcaption {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2);
    padding: var(--z-space-2) var(--z-space-3);
    border-bottom: 1px solid var(--z-border);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-muted);
  }
  pre {
    margin: 0;
    padding: var(--z-space-3);
    overflow-x: auto;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .hint {
    margin: 0;
    padding: 0 var(--z-space-3) var(--z-space-3);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-pending);
  }
</style>
