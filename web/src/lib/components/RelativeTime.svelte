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
  // One string, with its space in it: a space written between two blocks of markup
  // is trimmed away, and a `{' '}` to put it back is what the lint rightly calls
  // noise. A prefix that already ends in a space is not given two.
  const lead = $derived(prefix ? `${prefix.trimEnd()} ` : '');
  const title = $derived(formatTimestampTitle(value));
  const iso = $derived(typeof value === 'string' ? value : undefined);
</script>

<time class="relative {className}" class:plain datetime={iso} {title}>{lead}{text}</time>

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
