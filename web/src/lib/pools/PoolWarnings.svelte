<!--
  The warnings panel.

  Every dangerous setting the validator flagged, in the server's own words, with
  the consequence and the fix beside it. When there is nothing wrong this is one
  quiet line rather than an empty box or a green celebration.
-->
<script lang="ts">
  import { ShieldCheck } from '@lucide/svelte';
  import type { Problem } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';

  interface Props {
    warnings?: readonly Problem[];
    /** Shown when there is nothing to report. */
    okMessage?: string;
    /** Render without the surrounding card, for use inside another panel. */
    bare?: boolean;
    class?: string;
  }

  let {
    warnings = [],
    okMessage = 'Nothing dangerous is switched on for this pool.',
    bare = false,
    class: className = '',
  }: Props = $props();
</script>

<div class="warnings {className}" class:bare>
  {#if warnings.length === 0}
    <p class="ok">
      <ShieldCheck size={14} aria-hidden="true" />
      {okMessage}
    </p>
  {:else}
    <ul>
      {#each warnings as warning, index (warning.code + index)}
        {@const status = severityStatus(warning.severity)}
        <li>
          <div class="head">
            <Badge {status} size="sm" />
            <p class="title">{warning.title}</p>
          </div>
          {#if warning.detail}<p class="detail">{warning.detail}</p>{/if}
          {#if warning.fix}
            <p class="fix"><span class="fix-label">Fix</span>{warning.fix}</p>
          {/if}
          {#if warning.setting}
            <p class="setting"><code>{warning.setting}</code></p>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .warnings:not(.bare) {
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    padding: var(--z-space-5);
  }
  .ok {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    color: var(--z-text-muted);
  }
  ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    padding-left: var(--z-space-3);
    border-left: 2px solid var(--z-pending-border);
  }
  .head {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    flex-wrap: wrap;
  }
  .title {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .detail,
  .fix {
    margin: var(--z-space-1) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .fix-label {
    display: inline-block;
    margin-right: var(--z-space-2);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-idle);
  }
  .setting {
    margin: var(--z-space-1) 0 0;
  }
  .setting code {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
</style>
