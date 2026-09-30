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

  /**
   * The same two for a bound that may carry a time of day, `YYYY-MM-DDTHH:MM`
   * as a `datetime-local` input writes it, or be a bare date as an older link
   * does. An end at a minute takes the whole of that minute, as an end on a
   * day takes the whole of that day, so "until 17:00" includes the job that
   * finished at 17:00:40.
   */
  export function startOfMoment(value: string): string | undefined {
    if (!value.includes('T')) return startOfDay(value);
    const ms = new Date(value).getTime();
    return Number.isNaN(ms) ? undefined : new Date(ms).toISOString();
  }

  export function endOfMoment(value: string): string | undefined {
    if (!value.includes('T')) return endOfDay(value);
    const ms = new Date(value).getTime();
    return Number.isNaN(ms) ? undefined : new Date(ms + 59_999).toISOString();
  }

  /** An instant as a `datetime-local` input shows it, on the operator's own clock. */
  export function localMoment(at: Date): string {
    const local = new Date(at.getTime() - at.getTimezoneOffset() * 60_000);
    return local.toISOString().slice(0, 16);
  }
</script>

<!--
  A from/to pair of dates, or of dates and times of day with `withTime`.

  Native date inputs rather than a hand-rolled calendar: they are keyboard
  operable, localised and understood by every assistive technology already,
  which is more than a bespoke picker would manage. The Input component has no
  date type, so these are plain inputs wearing the same clothes.
-->
<script lang="ts">
  interface Props {
    /**
     * ISO calendar dates, `YYYY-MM-DD`, or empty for no bound. With `withTime`
     * they are `YYYY-MM-DDTHH:MM` on the operator's clock.
     */
    since: string;
    until: string;
    /** Pick a time of day as well as a date, for a range shorter than a day. */
    withTime?: boolean;
    /** Names the pair: "Jobs queued". */
    label: string;
    onchange: (next: { since: string; until: string }) => void;
    class?: string;
  }

  let { since, until, label, withTime = false, onchange, class: className = '' }: Props = $props();

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
    type={withTime ? 'datetime-local' : 'date'}
    value={since}
    max={until || undefined}
    aria-invalid={backwards}
    aria-describedby={backwards ? errorId : undefined}
    onchange={(event) => onchange({ since: event.currentTarget.value, until })}
  />
  <label class="leg" for={untilId}>to</label>
  <input
    id={untilId}
    type={withTime ? 'datetime-local' : 'date'}
    value={until}
    min={since || undefined}
    aria-invalid={backwards}
    aria-describedby={backwards ? errorId : undefined}
    onchange={(event) => onchange({ since, until: event.currentTarget.value })}
  />
  {#if backwards}
    <!-- An alert so the failure is spoken when it appears; the describedby link
         alone is only read if a field is revisited. -->
    <p class="error" id={errorId} role="alert">
      The end is before the start, so nothing can match.
    </p>
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
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    color: var(--z-text);
    font-family: var(--z-font-sans);
    font-size: var(--z-text-xs);
    color-scheme: inherit;
  }
  input:hover {
    border-color: var(--z-text-subtle);
  }
  .error {
    margin: 0;
    color: var(--z-danger);
    font-size: var(--z-text-xs);
  }
  /*
    16px on a phone, and only on a phone.

    The base control size is right for a dense operator UI on a desktop -- but
    mobile Safari zooms the whole viewport whenever a focused control's
    font-size is under 16px, and the viewport meta deliberately does not set
    maximum-scale. So every field tap jumped the 360px page to roughly 410px
    effective width and ran the card off both edges, once per field. Height
    comes from the space scale, so nothing reflows; only the glyphs grow.
  */
  @media (max-width: 768px) {
    input {
      font-size: var(--z-control-font-touch);
    }
  }
</style>
