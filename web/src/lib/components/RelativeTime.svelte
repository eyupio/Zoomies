<!--
  "4m ago", updating itself from one shared clock rather than one timer per row,
  with the exact timestamp in the title for anyone who needs it.
-->
<script lang="ts">
  import { onClockTick, formatTimestampTitle, relativeTime, type TimeInput } from '../format';

  interface Props {
    value: TimeInput;
    /** Text before the value: "started", "expires". */
    prefix?: string;
    /** Render as plain text with no dotted underline. */
    plain?: boolean;
    class?: string;
  }

  let { value, prefix, plain = false, class: className = '' }: Props = $props();

  let now = $state(Date.now());

  $effect(() => onClockTick((t) => (now = t)));

  const text = $derived(relativeTime(value, now));
  const title = $derived(formatTimestampTitle(value));
  const iso = $derived(typeof value === 'string' ? value : undefined);
</script>

<!--
  The space after a prefix is written here and not left to the markup: Svelte
  trims the whitespace at the end of a block, so a line break after the prefix
  inside the `{#if}` was never a space, and "created" ran into "just now". A
  prefix that ends in a space of its own (`since `) is not given two.
-->
<time class="relative {className}" class:plain datetime={iso} {title}
  >{#if prefix}{prefix.trimEnd()}{' '}{/if}{text}</time
>

<style>
  .relative {
    color: inherit;
    white-space: nowrap;
    border-bottom: var(--z-border-width) dotted var(--z-border-strong);
    cursor: help;
  }
  .relative.plain {
    border-bottom: 0;
    cursor: inherit;
  }
</style>
