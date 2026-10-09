<!--
  Draws what `parseMarkdown` returns.

  Every element here is made by this component and every string goes in as text,
  so nothing a model writes can become markup. A link opens in a new tab, says
  where it goes on hover, and carries no referrer.
-->
<script lang="ts">
  import type { Block, Inline } from './markdown';
  import { parseMarkdown } from './markdown';
  import CodeBlock from './CodeBlock.svelte';

  let { source }: { source: string } = $props();
  const tree = $derived(parseMarkdown(source));
</script>

{#snippet inline(nodes: Inline[])}
  {#each nodes as node, index (index)}
    {#if node.t === 'text'}{node.v}{:else if node.t === 'br'}<br
      />{:else if node.t === 'strong'}<strong>{@render inline(node.c)}</strong
      >{:else if node.t === 'em'}<em>{@render inline(node.c)}</em>{:else if node.t === 'code'}<code
        >{node.v}</code
      >{:else if node.t === 'a'}<a
        href={node.href}
        title={node.href}
        target="_blank"
        rel="noopener noreferrer nofollow">{@render inline(node.c)}</a
      >{/if}
  {/each}
{/snippet}

{#snippet block(node: Block)}
  {#if node.t === 'p'}
    <p>{@render inline(node.c)}</p>
  {:else if node.t === 'h'}
    <p class="heading" data-level={node.level}>{@render inline(node.c)}</p>
  {:else if node.t === 'code'}
    <CodeBlock lang={node.lang} code={node.v} />
  {:else if node.t === 'list'}
    {#if node.ordered}
      <ol start={node.start}>
        {#each node.items as item, index (index)}
          <li>
            {#each item as child, k (k)}{@render block(child)}{/each}
          </li>
        {/each}
      </ol>
    {:else}
      <ul>
        {#each node.items as item, index (index)}
          <li>
            {#each item as child, k (k)}{@render block(child)}{/each}
          </li>
        {/each}
      </ul>
    {/if}
  {:else if node.t === 'quote'}
    <blockquote>
      {#each node.c as child, k (k)}{@render block(child)}{/each}
    </blockquote>
  {:else if node.t === 'hr'}
    <hr />
  {:else if node.t === 'table'}
    <!-- A table that scrolls sideways must be reachable by keyboard. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="table" role="region" aria-label="Table" tabindex="0">
      <table>
        <thead>
          <tr>
            {#each node.head as cell, c (c)}
              <th style:text-align={node.align[c]}>{@render inline(cell)}</th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each node.rows as row, r (r)}
            <tr>
              {#each row as cell, c (c)}
                <td style:text-align={node.align[c]}>{@render inline(cell)}</td>
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/snippet}

<div class="markdown">
  {#each tree as node, index (index)}{@render block(node)}{/each}
</div>

<style>
  .markdown {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    min-width: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-base);
    overflow-wrap: anywhere;
  }
  p,
  ul,
  ol,
  blockquote {
    margin: 0;
  }
  .heading {
    font-weight: var(--z-weight-semibold);
  }
  .heading[data-level='1'] {
    font-size: var(--z-text-base);
  }
  /*
    Block, not flex: a flex container turns its items into boxes with no marker.
    The reset the whole UI starts from takes the markers off every list, so they
    are put back here, or a list is only indented text.
  */
  ul,
  ol {
    padding-left: var(--z-space-5);
  }
  ul {
    list-style: disc;
  }
  ol {
    list-style: decimal;
  }
  ul ul {
    list-style: circle;
  }
  li + li {
    margin-top: var(--z-space-1);
  }
  li > :global(* + *) {
    margin-top: var(--z-space-1);
  }
  li::marker {
    color: var(--z-text-muted);
  }
  blockquote {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding-left: var(--z-space-3);
    border-left: var(--z-border-width-rail) solid var(--z-border-strong);
    color: var(--z-text-muted);
  }
  hr {
    width: 100%;
    margin: 0;
    border: 0;
    border-top: var(--z-border-width) solid var(--z-border);
  }
  code {
    padding: 0.1em 0.35em;
    font-family: var(--z-font-mono);
    font-size: 0.9em;
    background: var(--z-surface-sunken);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    overflow-wrap: anywhere;
  }
  a {
    color: var(--z-accent);
    text-decoration: underline;
    text-underline-offset: var(--z-underline-offset);
  }
  .table {
    max-width: 100%;
    overflow-x: auto;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--z-text-xs);
  }
  th,
  td {
    padding: var(--z-space-2) var(--z-space-3);
    text-align: left;
    vertical-align: top;
    border-bottom: var(--z-border-width) solid var(--z-border);
    overflow-wrap: normal;
  }
  th {
    font-weight: var(--z-weight-semibold);
    background: var(--z-surface-sunken);
    white-space: nowrap;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
</style>
