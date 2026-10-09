<!--
  The hosts' rollout as far as it has got: a word and a colour for its state,
  how many hosts it has updated of how many, and the controller's sentence.

  Shown on the Hosts page, where an administrator resumes or cancels it, and on
  Settings → Updates beside the release it follows. Everything here is read off
  the status and never kept, so a reload or a second tab shows the same. The
  live region belongs to the page that holds this, because a region that
  arrives with its words already in it is not reliably read out.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import Badge from '$lib/components/Badge.svelte';
  import { rolloutWords, type Rollout } from './words';

  interface Props {
    rollout: Rollout;
    /** The planner's sentence, shown as it was given, or empty. */
    sentence?: string;
    /** Resume and Cancel, for the page that offers them. */
    actions?: Snippet;
  }

  let { rollout, sentence = '', actions }: Props = $props();

  const words = $derived(rolloutWords(rollout));
</script>

<div class="rollout" data-state={rollout.state}>
  <div class="head">
    <Badge tone={words.tone} label={words.label} dot={false} />
    <h3>{words.title}</h3>
  </div>
  <p class="progress">{words.progress}</p>
  <!-- Text from the controller: interpolated, so it can never be markup. -->
  {#if words.detail}<p class="detail">{words.detail}</p>{/if}
  {#if sentence && sentence !== words.detail}<p class="detail">{sentence}</p>{/if}
  {#if words.next}<p class="detail">{words.next}</p>{/if}
  {#if actions}<div class="actions">{@render actions()}</div>{/if}
</div>

<style>
  .rollout {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
    min-width: 0;
  }
  .rollout[data-state='halted'] {
    border-color: var(--z-draining-border);
    background: var(--z-draining-subtle);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .progress {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    font-variant-numeric: tabular-nums;
  }
  .detail {
    max-width: var(--z-measure-prose);
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin-top: var(--z-space-2);
  }
</style>
