<!--
  A humanised duration in tabular figures, so a column of them lines up.
  Give it milliseconds, or two timestamps, or a start with `live` to have it
  count while the job is still running.
-->
<script lang="ts">
  import { formatDuration, onClockTick, toMillis, type TimeInput } from '../format';

  interface Props {
    /** Milliseconds. Takes precedence over `from`/`to`. */
    ms?: number | null;
    from?: TimeInput;
    to?: TimeInput;
    /** Tick while it runs. Use for a job that has started but not finished. */
    live?: boolean;
    class?: string;
  }

  let { ms, from, to, live = false, class: className = '' }: Props = $props();

  let now = $state(Date.now());

  $effect(() => {
    if (!live) return;
    return onClockTick((t) => (now = t));
  });

  const value = $derived.by(() => {
    if (ms !== undefined && ms !== null) return ms;
    const start = toMillis(from);
    if (start === null) return null;
    const end = toMillis(to);
    return (end ?? (live ? now : Date.now())) - start;
  });
</script>

<span class="duration tabular {className}">{formatDuration(value)}</span>

<style>
  .duration {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
</style>
