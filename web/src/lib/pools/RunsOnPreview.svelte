<!--
  The workflow snippet these labels produce.

  Pool labels only mean something in relation to a `runs-on:` line, so the
  wizard shows that line as it is typed rather than describing it.

  It shows the shortest correct form. One branded label is enough to reach a
  pool -- `runs-on: zoomies-linux-x64` -- and it is what a workflow should
  write: the older `[self-hosted, linux, x64, ...]` habit is longer, says
  nothing about which fleet, and breaks the moment two pools share an
  architecture.
-->
<script lang="ts">
  import CopyButton from '$lib/components/CopyButton.svelte';
  import { BRAND_LABEL, isImplicit, normalizeLabels, runsOnSnippet } from '$lib/brand';

  interface Props {
    labels?: readonly string[];
    class?: string;
  }

  let { labels = [], class: className = '' }: Props = $props();

  const snippet = $derived(runsOnSnippet(labels));
  // A pool with nothing but the brand and the labels every runner already
  // advertises answers every job that asks for this fleet, which is rarely what
  // an operator meant. That is the state worth warning about -- not "no labels
  // typed yet", since the brand is always there.
  const specific = $derived(
    normalizeLabels(labels).filter((l) => !isImplicit(l) && l !== BRAND_LABEL),
  );
</script>

<figure class="preview {className}">
  <figcaption>
    <span>What a workflow writes</span>
    <CopyButton value={snippet} label="Copy the runs-on snippet" />
  </figcaption>
  <pre><code>{snippet}</code></pre>
  {#if specific.length === 0}
    <p class="hint">
      With no labels of its own this pool answers every job that asks for this fleet, which is
      rarely what an operator wants once there is more than one pool.
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
