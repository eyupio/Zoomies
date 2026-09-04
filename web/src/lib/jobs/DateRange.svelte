<script module lang="ts">
  /**
   * The API takes RFC 3339 instants; a person picks days. These two turn one
   * into the other in the operator's own time zone, so "since 4 September"
   * means their fourth of September and not UTC's.
   */
  export function startOfDay(date: string): string | undefined {
    if (!date) return undefined;
    const ms = new Date(`${date}T00:00:00`).getTime();
    return Number.isNaN(ms) ? undefined : new Date(ms).toISOString();
  }

  export function endOfDay(date: string): string | undefined {
    if (!date) return undefined;
    const ms = new Date(`${date}T23:59:59.999`).getTime();
    return Number.isNaN(ms) ? undefined : new Date(ms).toISOString();
  }
</script>

<!--
  A from/to pair of dates.

  Native date inputs rather than a hand-rolled calendar: they are keyboard
  operable, localised and understood by every assistive technology already,
  which is more than a bespoke picker would manage. The Input component has no
  date type, so these are plain inputs wearing the same clothes.
-->
<script lang="ts">
  interface Props {
    /** ISO calendar dates, `YYYY-MM-DD`, or empty for no bound. */
    since: string;
    until: string;
    /** Names the pair: "Jobs queued". */
    label: string;
    onchange: (next: { since: string; until: string }) => void;
    class?: string;
  }

  let { since, until, label, onchange, class: className = '' }: Props = $props();

  const uid = $props.id();
  const sinceId = `from-${uid}`;
  const untilId = `to-${uid}`;
  const errorId = `range-${uid}`;

  const backwards = $derived(Boolean(since && until && since > until));
</script>

<div class="range {className}" role="group" aria-label={label}>
  <label class="leg" for={sinceId}>From</label>
  <input
    id={sinceId}
    type="date"
    value={since}
    max={until || undefined}
    aria-invalid={backwards}
    aria-describedby={backwards ? errorId : undefined}
    onchange={(event) => onchange({ since: event.currentTarget.value, until })}
  />
  <label class="leg" for={untilId}>to</label>
  <input
    id={untilId}
    type="date"
    value={until}
    min={since || undefined}
    aria-invalid={backwards}
    aria-describedby={backwards ? errorId : undefined}
    onchange={(event) => onchange({ since, until: event.currentTarget.value })}
  />
  {#if backwards}
    <p class="error" id={errorId}>The end date is before the start date, so nothing can match.</p>
  {/if}
</div>

<style>
  .range {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .leg {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  input {
    height: var(--z-space-6);
    padding: 0 var(--z-space-2);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    color: var(--z-text);
    font-family: var(--z-font-sans);
    font-size: var(--z-text-xs);
    color-scheme: light dark;
  }
  input:hover {
    border-color: var(--z-text-subtle);
  }
  .error {
    margin: 0;
    color: var(--z-danger);
    font-size: var(--z-text-xs);
  }
</style>
