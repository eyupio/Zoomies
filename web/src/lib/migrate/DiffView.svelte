<!--
  A unified diff, as the server rendered it.

  This is the screen the whole wizard exists for. Everything before it is a
  guess about what an operator wants; this is the exact change that is about to
  appear in somebody else's repository, so it is shown as a diff rather than
  summarised in prose. Colour is doubled by the +/- gutter, because a red line
  and a green line that differ only in hue are two identical lines to a
  colour-blind reviewer.
-->
<script lang="ts">
  interface Props {
    diff?: string;
    class?: string;
  }

  let { diff = '', class: className = '' }: Props = $props();

  interface Line {
    kind: 'add' | 'remove' | 'context' | 'hunk' | 'file';
    text: string;
  }

  const lines = $derived.by<Line[]>(() =>
    diff
      .split('\n')
      .filter((line, i, all) => !(line === '' && i === all.length - 1))
      .map((text) => {
        if (text.startsWith('@@')) return { kind: 'hunk' as const, text };
        if (text.startsWith('---') || text.startsWith('+++'))
          return { kind: 'file' as const, text };
        if (text.startsWith('+')) return { kind: 'add' as const, text };
        if (text.startsWith('-')) return { kind: 'remove' as const, text };
        return { kind: 'context' as const, text };
      }),
  );
</script>

{#if diff}
  <!--
    The scroll container is a focusable group, not the <pre> itself: a region a
    mouse can scroll and a keyboard cannot is a WCAG 2.1.1 failure, and
    "group" is the role that carries a tabindex without pretending the diff is
    a control. The linter's rule does not know about that exception, so it is
    silenced here rather than by making the region unreachable.
  -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="diff {className}" tabindex="0" role="group" aria-label="The change to this file">
    <pre><code
        >{#each lines as line, i (i)}<span class="line {line.kind}"
            ><span class="sr-only"
              >{line.kind === 'add' ? 'added' : line.kind === 'remove' ? 'removed' : ''}</span
            >{line.text}
</span>{/each}</code
      ></pre>
  </div>
{/if}

<style>
  .diff {
    max-height: 22rem;
    overflow: auto;
    padding: var(--z-space-2) 0;
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-sm);
    tab-size: 2;
  }
  .diff pre {
    margin: 0;
  }
  .diff:focus-visible {
    outline: 2px solid var(--z-accent);
    outline-offset: 1px;
  }
  .line {
    display: block;
    padding: 0 var(--z-space-3);
    white-space: pre;
    color: var(--z-text-muted);
  }
  .add {
    background: var(--z-idle-subtle);
    color: var(--z-idle);
  }
  .remove {
    background: var(--z-danger-subtle);
    color: var(--z-danger);
  }
  .hunk {
    color: var(--z-text-subtle);
  }
  .file {
    color: var(--z-text-subtle);
  }
</style>
