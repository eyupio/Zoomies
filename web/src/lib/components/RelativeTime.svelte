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

<time class="relative {className}" class:plain datetime={iso} {title}>
  {#if prefix}{prefix}
  {/if}{text}
</time>

<style>
  .relative {
    color: inherit;
    white-space: nowrap;
    border-bottom: 1px dotted var(--z-border-strong);
    cursor: help;
  }
  .relative.plain {
    border-bottom: 0;
    cursor: inherit;
  }
</style>
