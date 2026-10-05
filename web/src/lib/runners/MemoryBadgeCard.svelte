<!--
  One memory badge, said in full: what it is, the sentence that explains it, the
  runner's memory as a bar and three figures, and for the folders a row each.

  It is drawn in two places that have very different amounts of room -- the card
  a pill opens on hover and focus, and the "Memory and folders" panel on the
  runner's page -- and is the same markup in both, so what an operator reads in
  the tooltip is what the page says. It is made of inline elements arranged with
  grids, because a tooltip's bubble is a span.
-->
<script lang="ts">
  import BadgeIcon from './BadgeIcon.svelte';
  import MemoryBar from './MemoryBar.svelte';
  import type { MemoryBadge } from './memory-badges';

  let { badge }: { badge: MemoryBadge } = $props();
</script>

<span class="card" data-tone={badge.tone}>
  <span class="head">
    <span class="glyph" class:dashed={badge.dashed}>
      <BadgeIcon icon={badge.icons[0] ?? 'memory'} size={16} />
    </span>
    <span class="titles">
      <span class="eyebrow">{badge.eyebrow}</span>
      <strong class="title">{badge.title}</strong>
    </span>
  </span>

  <span class="detail">{badge.detail}</span>

  {#if badge.bar}
    <MemoryBar bar={badge.bar} />
  {/if}

  {#if badge.figures.length > 0}
    <span class="figures">
      {#each badge.figures as figure (figure.label)}
        <span class="figure"><b>{figure.value}</b><small>{figure.label}</small></span>
      {/each}
    </span>
  {/if}

  {#if badge.folders.length > 0}
    <span class="folders">
      {#each badge.folders as folder (folder.kind)}
        <span class="folder" class:disk={!folder.inMemory}>
          <span class="folder-icon"><BadgeIcon icon={folder.icon} size={14} /></span>
          <span class="folder-name">{folder.label}</span>
          <span class="folder-size">{folder.size}</span>
          <span class="folder-path"
            ><code>{folder.path}</code>{#each folder.tags as tag (tag)}<span class="tag">{tag}</span
              >{/each}</span
          >
          {#if folder.note}<span class="folder-note">{folder.note}</span>{/if}
        </span>
      {/each}
    </span>
  {/if}

  {#each badge.notes as note (note)}
    <span class="note">{note}</span>
  {/each}
</span>

<style>
  .card {
    --card-colour: var(--z-neutral);
    --card-subtle: var(--z-neutral-subtle);
    --card-border: var(--z-neutral-border);
    display: grid;
    gap: var(--z-space-3);
    padding: var(--z-space-1);
    text-align: left;
    min-width: 0;
  }
  .card[data-tone='accent'] {
    --card-colour: var(--z-accent);
    --card-subtle: var(--z-accent-subtle);
    --card-border: var(--z-accent-border);
  }
  .card[data-tone='pending'] {
    --card-colour: var(--z-pending);
    --card-subtle: var(--z-pending-subtle);
    --card-border: var(--z-pending-border);
  }
  .card[data-tone='danger'] {
    --card-colour: var(--z-danger);
    --card-subtle: var(--z-danger-subtle);
    --card-border: var(--z-danger-border);
  }
  .head {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    min-width: 0;
  }
  .glyph {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: var(--z-status-icon-size);
    height: var(--z-status-icon-size);
    border: var(--z-border-width) solid var(--card-border);
    border-radius: var(--z-radius-md);
    background: var(--card-subtle);
    color: var(--card-colour);
  }
  .glyph.dashed {
    border-style: dashed;
  }
  .titles {
    display: grid;
    min-width: 0;
  }
  .eyebrow {
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }
  .title {
    color: var(--z-text);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    line-height: var(--z-leading-sm);
  }
  .detail {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .figures {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(0, 1fr));
    gap: var(--z-space-3);
    padding-top: var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .figure {
    display: grid;
    gap: var(--z-nudge-2);
    min-width: 0;
  }
  .figure b {
    color: var(--z-text);
    font-size: var(--z-text-sm);
    font-variant-numeric: tabular-nums;
  }
  .figure small {
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
  }
  .folders {
    display: grid;
    gap: var(--z-space-3);
    padding-top: var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .folder {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    column-gap: var(--z-space-2);
    row-gap: var(--z-nudge-2);
    align-items: center;
  }
  .folder-icon {
    display: inline-flex;
    grid-row: 1;
    align-items: center;
    justify-content: center;
    width: var(--z-space-6);
    height: var(--z-space-6);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text);
  }
  .folder.disk .folder-icon {
    color: var(--z-text-muted);
  }
  .folder-name {
    color: var(--z-text);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
  }
  .folder-size {
    color: var(--z-text);
    font-size: var(--z-text-xs);
    font-variant-numeric: tabular-nums;
    font-weight: var(--z-weight-semibold);
  }
  .folder.disk .folder-size {
    color: var(--z-text-muted);
    font-weight: var(--z-weight-normal);
  }
  .folder-path {
    grid-column: 2 / -1;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-1) var(--z-space-2);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }
  .folder-path code {
    font-family: var(--z-font-mono);
  }
  .tag {
    padding: 0 var(--z-space-1);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-2xs);
  }
  .folder-note {
    grid-column: 2 / -1;
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }
  .note {
    padding-left: var(--z-space-3);
    border-left: var(--z-border-width-thick) solid var(--card-border);
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
</style>
