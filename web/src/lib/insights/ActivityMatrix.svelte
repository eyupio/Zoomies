<!--
  The activity matrix: a contribution graph of the fleet's jobs.

  One square per day, laid out as a calendar -- a column per week, a row per
  weekday, the month named above the week it begins in -- so a year of CI
  reads at a glance the way a year of commits does. A shorter window cuts
  each day into several squares side by side, four-hourly or eight-hourly,
  so a month is a band of squares rather than five columns of them, and
  says whether the fleet's work lands in the morning or overnight. The
  squares are coloured
  by what finished: greener as more jobs finish, red the moment any fail, and
  darker red the larger the share. Three other colourings are a select away,
  because "when does the queue back up?" and "when does the pool hit its
  ceiling?" are the same shape with a different figure behind it.

  Colour is never the only carrier. A failing square has a hole in it, a square
  with work waiting and nothing finished is hollow, a square with no capacity
  telemetry is dashed, and every square's accessible name is the whole sentence
  the tooltip shows, so nothing lives only in the tooltip. The grid is one tab
  stop: the arrow keys walk the squares, Enter selects one, and Escape lets go.

  Selecting a square opens the detail beneath the grid: the figures, the day's
  hour-by-hour breakdown when a loader is given -- the hours of a slice picked
  out from the rest of its day -- and links into the pages that list the jobs
  themselves. The same component draws the Usage page's matrix, where a window
  of two days or less is laid out an hour per square instead.

  The hour-by-hour breakdown is itself a shortcut, not only a picture: hovering
  a bar shows the same floating figures a square does, and clicking one asks
  `links` the question a square's own list already answers, so a bar goes
  wherever "Jobs that day" or "Failed jobs" would -- the red segment of a bar
  to the failures, the rest of it to the day. Nothing here reaches for a
  tooltip library: the one popover card above already carries a square's whole
  story, an hour is the same card asked about a narrower moment, and a second
  widget would say nothing it does not.

  The band is the grid, the aside beside it, and the key under both. A grid
  with at least as many columns as rows grows its square until it fills the
  room it is given, so a day of hours is a band across the panel rather than a
  stub at the left; one too narrow for that -- a few weeks of whole days,
  which is five week columns however large the square -- keeps its size and
  hands the rest over. A grid that would not fit at its size shrinks instead,
  so the whole window is always on screen: a year on a phone is a year of
  four-pixel squares, not the half of it that fitted at ten.
  The aside is the window in words, what the squares on screen come to, and it
  takes whatever the grid did not: its figures are fluid, so the same markup
  reads as a column beside a wide grid and as a row beside a narrow one. The
  key goes underneath, where it explains the squares and fits on one line.

  A band too narrow for the pair of them -- a phone -- gives the width to
  whichever of the two would use it, and that is measured rather than assumed:
  a grid that would fill the band takes it and the figures go under, a grid
  that would stop short of it keeps them beside. Both stacked was the old rule
  and it was wrong both ways round, a week of hours at ten pixels across half a
  screen and a few weeks as a stub with the rest of the screen white beside it.
-->
<script lang="ts" module>
  export interface ActivityLink {
    href: string;
    label: string;
  }
  export interface ActivityRange {
    from: Date;
    to: Date;
    bucket: ActivityBucket;
  }
</script>

<script lang="ts">
  import { tick, untrack, type Snippet } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import { X } from '@lucide/svelte';
  import { formatNumber, formatPercent } from '$lib/format';
  import { layers } from '$lib/keys';
  import { router } from '$lib/router';
  import IconButton from '$lib/components/IconButton.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import {
    addDays,
    calendar,
    completedIn,
    describe,
    headline,
    hourGrid,
    hours,
    intervalName,
    intervalNoun,
    intervalWidth,
    monthLabels,
    paint,
    scaleOf,
    slicesPerDay,
    startOfLocalDay,
    summarise,
    type ActivityBucket,
    type ActivityMode,
    type CalendarCell,
    type Interval,
  } from './activity';

  interface Props {
    /** The complete series, one bucket per interval, oldest first. */
    buckets: readonly ActivityBucket[];
    interval: Interval;
    /** The local day bucket 0 belongs to. Only the calendar layout reads it. */
    first: Date;
    mode?: ActivityMode;
    /**
     * Calendar layout only: the trailing week columns to show, or all of them.
     * Never fewer to make them fit -- a year with months cut off the front is
     * not a year -- so the square shrinks to the width instead.
     */
    weeks?: number | 'all';
    /**
     * How big a square would like to be. A year is fifty-two columns and gets
     * the ten-pixel square a contribution graph taught everyone to read; a
     * day of hours has room for a larger one. The caller decides from the
     * range, never from what happens to fit, or a narrow screen would change
     * the square and the square would change what fits. The width still has
     * the last word: a grid that would not fit at this size shrinks until it
     * does, and one with room to spare grows.
     */
    size?: 'sm' | 'md' | 'lg';
    /** The squares on screen, for a caller summing them up. */
    visible?: readonly ActivityBucket[];
    selected?: number | null;
    onselect?: (index: number | null) => void;
    /** Loads a day's hourly buckets for the detail. Daily layout only. */
    hourly?: (day: Date) => Promise<ActivityBucket[]>;
    /**
     * When the series was fetched. A new value throws the hourly cache away,
     * so a day reloaded from the server is not shown against hours from
     * before the reload.
     */
    fetchedAt?: number;
    /**
     * Where a selected interval's jobs can be seen in full. Asked once for the
     * selected square and, in the hourly breakdown, once per bar a click
     * lands on -- an hour's own link, from a caller that only ever answers in
     * days, so a bar's click goes to the day it belongs to.
     */
    links?: (range: ActivityRange) => ActivityLink[];
    /** Whose jobs these are, for the grid's accessible name. */
    subject?: string;
    /** A line above the legend: the totals of what is on screen, say. */
    caption?: Snippet;
    class?: string;
  }

  let {
    buckets,
    interval,
    first,
    mode = 'outcomes',
    weeks = 'all',
    size = 'sm',
    visible = $bindable([]),
    selected = $bindable(null),
    onselect,
    hourly,
    fetchedAt = 0,
    links,
    subject = 'jobs',
    caption,
    class: className = '',
  }: Props = $props();

  /* -- layout ------------------------------------------------------------- */

  interface Row {
    header: string;
    /** Whether the header is drawn. Every row has one for assistive technology. */
    shown: boolean;
    cells: Array<CalendarCell | null>;
  }

  const WEEKDAY = new Intl.DateTimeFormat(undefined, { weekday: 'short' });
  const DAY_SHORT = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' });
  // Short enough to sit in a figure's value beside its label, which is the
  // whole constraint: a bucket is a whole hour, so the minutes say nothing,
  // and an hourly window is a week at most, so the weekday places it without
  // the date. The long form belongs to the tooltip, which has a line of its
  // own to spend on it.
  const DAY_HOUR = new Intl.DateTimeFormat(undefined, { weekday: 'short', hour: 'numeric' });
  // The first of January 2024 was a Monday, which is all this needs it for.
  const weekdays = Array.from({ length: 7 }, (_, i) => WEEKDAY.format(new Date(2024, 0, 1 + i)));
  const hourLabels = Array.from({ length: 24 }, (_, h) =>
    h % 6 === 0 ? `${String(h).padStart(2, '0')}:00` : null,
  );

  /**
   * How many squares a day of the calendar is. Hours are the one width drawn
   * the other way round -- a row per day, a column per hour -- so they are
   * never sliced.
   */
  const slices = $derived(interval === 'hour' ? 1 : slicesPerDay(interval));
  const allColumns = $derived(interval === 'hour' ? [] : calendar(buckets, first, slices));

  /** The square the grid grows to if it is to fill the band, in pixels. */
  let room = $state<number | null>(null);
  /** The largest square at which the whole grid fits the band, in pixels. */
  let fit = $state<number | null>(null);
  /** The row headings' track, so the labels above start where the columns do. */
  let lead = $state<number | null>(null);
  /** Whether the columns are too close together for a month's short name. */
  let narrow = $state(false);
  /**
   * Whether a column of figures fits beside the grid at the widest the grid
   * will ever be. Where the band is too narrow for the pair, one of them has
   * to give up the width, and this says which -- see the narrow-band rules in
   * the stylesheet.
   */
  let roomy = $state(false);
  let bandWidth = $state(0);
  let band = $state<HTMLDivElement | null>(null);
  let aside = $state<HTMLDivElement | null>(null);
  let grid = $state<HTMLDivElement | null>(null);
  let labelRow = $state<HTMLDivElement | null>(null);
  /** A square at its ceiling, so the ceiling can be read back in pixels. */
  let ceiling = $state<HTMLSpanElement | null>(null);

  const columns = $derived.by(() => {
    if (interval === 'hour' || weeks === 'all' || weeks >= allColumns.length) return allColumns;
    return allColumns.slice(-Math.max(1, weeks));
  });

  /**
   * The grid as rows of cells, a column of `slices` cells under each label.
   * A row's cells run left to right through the whole window, so the
   * keyboard walks a day's slices and then on into the same weekday of the
   * next week, and the markup groups them back into days.
   */
  const layout = $derived.by((): { rows: Row[]; labels: Array<string | null>; slices: number } => {
    if (interval !== 'hour') {
      return {
        labels: monthLabels(columns, narrow ? 'narrow' : 'short'),
        slices,
        rows: weekdays.map((header, r) => ({
          header,
          shown: r % 2 === 0,
          cells: columns.flatMap((c) => c.cells.slice(r * slices, (r + 1) * slices)),
        })),
      };
    }
    return {
      labels: hourLabels,
      slices: 1,
      rows: hourGrid(buckets).map((row) => ({
        header: DAY_SHORT.format(row.date),
        shown: true,
        cells: row.cells,
      })),
    };
  });
  /** How many cells wide the grid is: every label's column of slices. */
  const across = $derived(layout.labels.length * layout.slices);

  /** A row's cells cut back into columns, each one day's slices or one hour. */
  function slotsOf(cells: Array<CalendarCell | null>): Array<Array<CalendarCell | null>> {
    const per = layout.slices;
    return layout.labels.map((_, c) => cells.slice(c * per, (c + 1) * per));
  }

  const cells = $derived(
    layout.rows.flatMap((r) => r.cells).filter((c): c is CalendarCell => c !== null),
  );
  const positions = $derived.by(() => {
    const map = new SvelteMap<number, { row: number; col: number }>();
    layout.rows.forEach((row, r) =>
      row.cells.forEach((cell, c) => {
        if (cell) map.set(cell.index, { row: r, col: c });
      }),
    );
    return map;
  });
  const byIndex = $derived(new SvelteMap(cells.map((c) => [c.index, c])));

  $effect(() => {
    visible = cells.map((c) => c.bucket);
  });

  /** The darkest square is the busiest one on screen, not one scrolled away. */
  const scale = $derived(scaleOf(visible));

  /*
    Measured rather than assumed. The gaps are tokens and the row heading is
    whatever the weekday names come to in the operator's language; reading
    them off the rendered grid is what keeps this file from repeating the
    token file's numbers.

    None of them depends on the size of the square -- the heading is the
    track before the first column, and the gaps are the gaps -- so a square
    this measurement changes moves nothing it measured, and there is no
    second pass to settle.
  */
  $effect(() => {
    if (!grid || !band || !aside || bandWidth === 0) return;
    const slot = grid.querySelector<HTMLElement>('.slot');
    if (!slot) return;
    const heading = slot.getBoundingClientRect().left - grid.getBoundingClientRect().left;
    const gap = parseFloat(getComputedStyle(grid).columnGap) || 0;
    lead = Math.max(0, heading - gap);
    const inner = parseFloat(getComputedStyle(slot).columnGap) || 0;
    const per = layout.slices;
    const count = Math.max(1, layout.labels.length);
    /** The square at which `width` holds every column, its slices and its gap. */
    const square = (width: number) => Math.max(0, (width / count - gap - (per - 1) * inner) / per);
    /*
      The band's width, not the frame's: the frame is a max-content track, so
      measuring it would be measuring the grid that is about to be sized
      against it. What the aside is owed comes off first, and the two ways of
      spending the width owe it different amounts.

      Fitting owes the aside its least width -- one column of figures --
      because every pixel given to it is taken off squares that are already
      smaller than they were asked to be. Growing a square owes it twice that,
      the width of two columns: the square is already legible and the gain is
      a nicety, so it stops while the aside can still be short and wide rather
      than narrow and tall. Where the aside is under the grid rather than
      beside it, neither owes it anything.
    */
    const least = hasAside ? parseFloat(getComputedStyle(aside).minWidth) || 0 : 0;
    const between = parseFloat(getComputedStyle(band).columnGap) || 0;
    const column = least > 0 ? least + between : 0;
    /*
      Whether the two of them fit. The widest the grid can ever be is every
      square at its ceiling -- read off a square kept at it, because the
      ceiling is a token and this is arithmetic -- or, for a grid that never
      grows, the width it already has. A band with that and a column of
      figures in it keeps them side by side; a band without has to choose, and
      the stylesheet's narrow-band rules do, from this.
    */
    const top = ceiling?.getBoundingClientRect().width ?? 0;
    const widest = grows
      ? heading + count * (per * top + (per - 1) * inner + gap)
      : grid.getBoundingClientRect().width;
    roomy = column > 0 && bandWidth - widest >= column;
    /*
      What the aside is owed, which is nothing where it is not beside the grid.
      Read off the rendered band rather than assumed -- the rule that stacks it
      is a media query, and a breakpoint repeated here is a breakpoint that
      drifts -- and measured again whenever the decision above changes, because
      until it has been through the DOM the band is still laid out the old way
      and the width read from it is the width of the wrong layout.
    */
    void roomy;
    const beside = aside.getBoundingClientRect().top < grid.getBoundingClientRect().bottom;
    const owed = beside ? column : 0;
    // One gap of slack for the frame's own padding: a square clipped by a
    // pixel is a square the frame grows a scrollbar for.
    const spare = bandWidth - heading - gap;
    room = square(spare - 2 * owed);
    fit = square(spare - owed);
  });

  /*
    The month names, once the squares have settled on a size. Read off the
    rendered columns rather than worked out, because the size is a clamp of
    tokens the stylesheet resolves -- and read after the next tick, because
    this effect runs in the same pass as the one above that sets the size,
    before the render that applies it. A month is at least four columns, and
    a short month name is up to three ems -- "Sept" is four letters in some
    languages -- so columns closer than that take the one-letter names. The
    names do not move the columns, so there is no second pass.
  */
  $effect(() => {
    void fit;
    void room;
    const el = grid;
    const row = labelRow;
    if (!el || !row || interval === 'hour') return;
    let gone = false;
    void tick().then(() => {
      if (gone) return;
      const slots = el.querySelectorAll<HTMLElement>('.slot');
      const a = slots[0];
      const b = slots[1];
      if (!a || !b || a.parentElement !== b.parentElement) return;
      const pitch = b.getBoundingClientRect().left - a.getBoundingClientRect().left;
      const em = parseFloat(getComputedStyle(row).fontSize) || 0;
      narrow = pitch > 0 && 4 * pitch < 3 * em;
    });
    return () => {
      gone = true;
    };
  });

  /** Whether the grid is at least as wide as it is tall, in squares. */
  const wide = $derived(layout.rows.length <= across);
  /**
   * Whether the square grows at all, which is what the width the grid wants
   * turns on. Read lazily, like everything else that asks about the aside:
   * what the aside holds is settled further down, with the words in it.
   */
  const grows = $derived.by(() => wide || !hasAside);

  /*
    The square grows only where the width is the grid's to take. A taller
    grid than it is wide -- a few weeks of whole days, which is five week
    columns whatever the square -- would reach the foot of the panel long
    before the right of it, so it keeps its size and the aside takes the
    width instead. With nothing beside it to take the width, though, even
    that grid grows: the alternative is a stub of squares and a field of
    white, and the ceiling below stops a square becoming a tile.

    Shrinking is the other half, and every grid does it: one that would not
    fit at the size asked for comes down until it does, so the newest square
    is on screen without the frame scrolling to it. The floor below stops a
    square becoming a speck; only past that does the frame scroll.
  */
  const fills = $derived(grows && room !== null);

  const weeksShown = $derived(interval === 'hour' ? 0 : columns.length);
  const gridLabel = $derived(
    interval === 'hour'
      ? `${layout.rows.length} ${layout.rows.length === 1 ? 'day' : 'days'} of ${subject}, one square per hour`
      : `${weeksShown} weeks of ${subject}, one square per ${
          slices === 1 ? 'day' : `${intervalWidth(interval) / intervalWidth('hour')} hours`
        }`,
  );

  /* -- keyboard ------------------------------------------------------------ */

  let root = $state<HTMLDivElement | null>(null);
  let focusIndex = $state<number | null>(null);

  // One tab stop: the newest square with anything in it, so a keyboard
  // arriving at the grid lands on today rather than on a blank Monday a
  // year ago.
  $effect(() => {
    if (cells.length === 0) {
      focusIndex = null;
      return;
    }
    const current = untrack(() => focusIndex);
    if (current !== null && byIndex.has(current)) return;
    const latest = (list: CalendarCell[]) =>
      list.reduce((best, c) => (c.index > best.index ? c : best), list[0]!);
    const busy = cells.filter(
      (c) => completedIn(c.bucket) > 0 || c.bucket.queued > 0 || c.bucket.started > 0,
    );
    focusIndex = latest(busy.length ? busy : cells).index;
  });

  function cellAt(row: number, col: number): CalendarCell | null {
    return layout.rows[row]?.cells[col] ?? null;
  }

  /** The next square in a direction, stepping over the blanks of a partial week. */
  function step(from: CalendarCell, dr: number, dc: number): CalendarCell | null {
    const pos = positions.get(from.index);
    if (!pos) return null;
    let { row, col } = pos;
    for (;;) {
      row += dr;
      col += dc;
      if (row < 0 || row >= layout.rows.length || col < 0 || col >= across) {
        return null;
      }
      const next = cellAt(row, col);
      if (next) return next;
    }
  }

  function endOfRow(from: CalendarCell, last: boolean): CalendarCell | null {
    const pos = positions.get(from.index);
    if (!pos) return null;
    const row = layout.rows[pos.row]?.cells ?? [];
    const found = last ? [...row].reverse().find(Boolean) : row.find(Boolean);
    return found ?? null;
  }

  async function moveFocus(cell: CalendarCell): Promise<void> {
    focusIndex = cell.index;
    await tick();
    root?.querySelector<HTMLElement>(`[data-index="${cell.index}"]`)?.focus();
  }

  function onKeydown(event: KeyboardEvent, cell: CalendarCell): void {
    let target: CalendarCell | null;
    switch (event.key) {
      case 'ArrowRight':
        target = step(cell, 0, 1);
        break;
      case 'ArrowLeft':
        target = step(cell, 0, -1);
        break;
      case 'ArrowDown':
        target = step(cell, 1, 0);
        break;
      case 'ArrowUp':
        target = step(cell, -1, 0);
        break;
      case 'Home':
        target = endOfRow(cell, false);
        break;
      case 'End':
        target = endOfRow(cell, true);
        break;
      case 'Enter':
      case ' ':
        event.preventDefault();
        select(cell.index);
        return;
      case 'Escape':
        // One thing at a time, innermost first: the tooltip, then the
        // selection. The shell's own Escape closes overlays, and there is
        // no overlay here, so the key is taken only when it did something.
        if (tip) {
          event.stopPropagation();
          hideTip();
        } else if (selected !== null) {
          event.stopPropagation();
          select(null);
        }
        return;
      default:
        return;
    }
    event.preventDefault();
    if (target) void moveFocus(target);
  }

  /* -- selection ------------------------------------------------------------ */

  function select(index: number | null): void {
    selected = selected === index ? null : index;
    onselect?.(selected);
  }

  const chosen = $derived(selected === null ? null : (byIndex.get(selected) ?? null));

  /** Where a square's interval starts and ends, for the links out of it. */
  function rangeOf(cell: CalendarCell): ActivityRange {
    const from = cell.date;
    const to =
      interval === 'day' ? addDays(from, 1) : new Date(from.getTime() + intervalWidth(interval));
    return { from, to, bucket: cell.bucket };
  }

  /*
    A bar in the hourly timeline is a shortcut into the jobs it stands for, not
    a second place that files its own request: `links` already knows how to
    turn an interval into the Jobs page's filters, so an hour asks it the same
    question a day does. The Jobs page filters by calendar day, not by hour, so
    every bar in a day's timeline resolves to the same day -- the bar picks
    which of that day's links fits what was actually clicked, not a finer
    window than the page can answer.

    The failed segment is the one link an operator reaches for from here: a
    red hole in the calendar is "what broke", and clicking the hole should not
    hand back the whole day undifferentiated. Anywhere else on the bar -- the
    other outcomes, the hollow queued marker, the bar's own padding -- is "what
    ran then", so it takes the first link `links` offers, same as the day
    detail's own list does.
  */
  function hourHref(bucket: ActivityBucket, at: Date, wantFailed: boolean): string | null {
    if (!links) return null;
    const range: ActivityRange = {
      from: at,
      to: new Date(at.getTime() + intervalWidth('hour')),
      bucket,
    };
    const out = links(range);
    if (out.length === 0) return null;
    if (wantFailed) return out.find((l) => l.href.includes('failed=true'))?.href ?? out[0]!.href;
    return out[0]!.href;
  }

  function clickHour(event: MouseEvent, bucket: ActivityBucket, at: Date): void {
    const wantFailed = (event.target as HTMLElement).closest('[data-tone="danger"]') !== null;
    const href = hourHref(bucket, at, wantFailed);
    if (href) router.navigate(href);
  }

  /* -- the hourly breakdown ------------------------------------------------- */

  interface Hours {
    key: number;
    buckets: ActivityBucket[] | null;
    failed: boolean;
  }
  let hoursFor = $state<Hours>({ key: Number.NaN, buckets: null, failed: false });
  const hoursCache = new SvelteMap<number, ActivityBucket[]>();
  let hoursAttempt = $state(0);

  $effect(() => {
    // A fresh series means fresh hours: today's were counted before the reload.
    void fetchedAt;
    hoursCache.clear();
  });

  $effect(() => {
    const loader = hourly;
    const cell = chosen;
    void hoursAttempt;
    if (!loader || interval === 'hour' || !cell) return;
    // A slice of a day shows the whole day's hours, with its own picked out:
    // eight hours drawn alone would be a bar chart with no context.
    const day = startOfLocalDay(cell.date);
    const key = day.getTime();
    const cached = hoursCache.get(key);
    if (cached) {
      hoursFor = { key, buckets: cached, failed: false };
      return;
    }
    hoursFor = { key, buckets: null, failed: false };
    let disposed = false;
    loader(day)
      .then((result) => {
        if (disposed) return;
        hoursCache.set(key, result);
        hoursFor = { key, buckets: result, failed: false };
      })
      .catch(() => {
        if (!disposed) hoursFor = { key, buckets: null, failed: true };
      });
    return () => {
      disposed = true;
    };
  });

  const hoursShown = $derived(
    chosen && hoursFor.key === startOfLocalDay(chosen.date).getTime() ? hoursFor : null,
  );
  /** Whether an hour of the day's breakdown falls outside the selected slice of it. */
  function outside(at: Date): boolean {
    if (!chosen || slices === 1) return false;
    const range = rangeOf(chosen);
    return at.getTime() < range.from.getTime() || at.getTime() >= range.to.getTime();
  }
  const hourPeak = $derived(
    Math.max(1, ...(hoursShown?.buckets ?? []).map((h) => Math.max(completedIn(h), h.queued))),
  );
  const hoursSummary = $derived.by(() => {
    const list = hoursShown?.buckets ?? [];
    if (list.length === 0) return '';
    const busiest = list.reduce((a, b) => (completedIn(b) > completedIn(a) ? b : a));
    const worst = list.reduce((a, b) => (b.failed > a.failed ? b : a));
    const at = (b: ActivityBucket) =>
      new Date(b.from).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
    const parts = [
      completedIn(busiest) > 0
        ? `busiest at ${at(busiest)} with ${formatNumber(completedIn(busiest))} finished`
        : 'nothing finished in any hour',
    ];
    if (worst.failed > 0)
      parts.push(`most failures at ${at(worst)}, ${formatNumber(worst.failed)}`);
    return `Hour by hour: ${parts.join('; ')}.`;
  });

  /* -- the tooltip ---------------------------------------------------------- */

  interface Tip {
    bucket: ActivityBucket;
    at: Date;
    interval: Interval;
    anchor: HTMLElement;
    /** The square's index in the series, or null for a bar in the detail. */
    index: number | null;
  }
  let tip = $state<Tip | null>(null);
  let tipEl = $state<HTMLDivElement | null>(null);
  let hoverAnchor: HTMLElement | null = null;

  function showTip(next: Tip): void {
    tip = next;
  }
  function hideTip(): void {
    tip = null;
  }
  function enter(
    el: HTMLElement,
    bucket: ActivityBucket,
    at: Date,
    which: Interval,
    index: number | null = null,
  ): void {
    hoverAnchor = el;
    showTip({ bucket, at, interval: which, anchor: el, index });
  }
  function leave(el: HTMLElement): void {
    hoverAnchor = null;
    // A square the keyboard is on keeps its tooltip when the pointer wanders
    // off; one a mouse clicked does not, or every selection would leave a
    // card hanging over the grid.
    if (tip?.anchor === el && !el.matches(':focus-visible')) hideTip();
  }
  function blur(el: HTMLElement): void {
    if (tip?.anchor === el && hoverAnchor !== el) hideTip();
  }

  // One floating card for the whole grid, in the browser's top layer so the
  // frame's own scrolling cannot clip it, positioned above its square and
  // flipped below when the top of the window is too close.
  $effect(() => {
    if (!tip || !tipEl) return;
    const anchor = tip.anchor;
    const card = tipEl;
    void tip.bucket;
    const layer = layers.push('tooltip', hideTip);
    card.showPopover();

    function position(): void {
      const rect = anchor.getBoundingClientRect();
      const width = document.documentElement.clientWidth;
      const height = document.documentElement.clientHeight;
      if (rect.bottom <= 0 || rect.top >= height || rect.right <= 0 || rect.left >= width) {
        hideTip();
        return;
      }
      for (let parent = anchor.parentElement; parent; parent = parent.parentElement) {
        const style = getComputedStyle(parent);
        const clip = parent.getBoundingClientRect();
        if (
          (/auto|scroll|hidden|clip/.test(style.overflowX) &&
            (rect.right <= clip.left || rect.left >= clip.right)) ||
          (/auto|scroll|hidden|clip/.test(style.overflowY) &&
            (rect.bottom <= clip.top || rect.top >= clip.bottom))
        ) {
          hideTip();
          return;
        }
      }
      const gap = parseFloat(getComputedStyle(card).paddingLeft);
      const bounds = card.getBoundingClientRect();
      const above = rect.top - bounds.height - gap;
      const y = above >= gap ? above : rect.bottom + gap;
      const x = rect.left + (rect.width - bounds.width) / 2;
      card.style.left = `${Math.max(gap, Math.min(x, width - bounds.width - gap))}px`;
      card.style.top = `${Math.max(gap, Math.min(y, height - bounds.height - gap))}px`;
    }

    position();
    document.addEventListener('scroll', position, true);
    window.addEventListener('resize', position);
    const observer = new ResizeObserver(position);
    observer.observe(anchor);
    observer.observe(card);
    return () => {
      layers.remove(layer);
      observer.disconnect();
      document.removeEventListener('scroll', position, true);
      window.removeEventListener('resize', position);
      if (card.matches(':popover-open')) card.hidePopover();
    };
  });

  /* -- words ---------------------------------------------------------------- */

  const OUTCOMES = [
    { key: 'succeeded', label: 'Succeeded', tone: 'idle' },
    { key: 'failed', label: 'Failed', tone: 'danger' },
    { key: 'cancelled', label: 'Cancelled or skipped', tone: 'neutral' },
    { key: 'unknown', label: 'Unknown', tone: 'pending' },
  ] as const;

  /** The figures a square carries, as label and value pairs. */
  function figures(b: ActivityBucket): Array<[string, string]> {
    const rows: Array<[string, string]> = [
      ['Queued', formatNumber(b.queued)],
      ['Started', formatNumber(b.started)],
      ['Finished', formatNumber(completedIn(b))],
      ['Executing', b.execution_seconds > 0 ? hours(b.execution_seconds) : '--'],
    ];
    if (b.allocated_seconds > 0) {
      rows.push(['Runner-hours', hours(b.allocated_seconds)]);
      // A share is only a share while the runner records are all there.
      // Runners are kept for less time than jobs, so an older day can have
      // its jobs' hours and only some of its runners', and the ratio is then
      // a number that means nothing rather than a percentage.
      if (b.allocated_seconds >= b.execution_seconds) {
        rows.push(['Busy share', formatPercent(b.execution_seconds / b.allocated_seconds)]);
      }
    }
    rows.push([
      'At capacity',
      b.capacity_samples > 0
        ? `${formatNumber(b.capacity_reached)} of ${formatNumber(b.capacity_samples)} min`
        : 'Not observed',
    ]);
    return rows;
  }

  /*
    What the squares on screen come to, for the aside beside them. The grid
    says when the fleet was busy; these say how much, how often it went wrong
    and where the worst of it landed -- the things an operator would otherwise
    get only by hovering every square in turn. A row that has nothing to say
    is left out rather than printed as a dash, so a quiet window is a short
    list and not a wall of nothing, and capacity waits until it has been
    reached at least once: a fleet that never hit its ceiling has a whole line
    of the band saying so, and the band is above the fleet's own numbers.
  */
  const summary = $derived.by((): Array<[string, string]> => {
    const totals = summarise(cells.map((c) => c.bucket));
    // The busiest day is a day even where a day is several squares: it is
    // the question an operator asks of a month, and a slice of a day would
    // answer a narrower one than the label says.
    const days: Record<number, { date: Date; done: number }> = {};
    let peak = 0;
    let samples = 0;
    let reached = 0;
    for (const cell of cells) {
      const key = interval === 'hour' ? cell.date.getTime() : startOfLocalDay(cell.date).getTime();
      const day = (days[key] ??= { date: cell.date, done: 0 });
      day.done += completedIn(cell.bucket);
      peak = Math.max(peak, cell.bucket.queued);
      samples += cell.bucket.capacity_samples;
      reached += cell.bucket.capacity_reached;
    }
    let busiest: { date: Date; done: number } | null = null;
    for (const day of Object.values(days)) {
      if (day.done > (busiest?.done ?? 0)) busiest = day;
    }
    const most = busiest?.done ?? 0;
    const rows: Array<[string, string]> = [];
    if (totals.completed > 0) {
      rows.push(['Failure rate', formatPercent(totals.failed / totals.completed, 1)]);
    }
    if (totals.execution_seconds > 0) rows.push(['Executing', hours(totals.execution_seconds)]);
    if (busiest) {
      rows.push([
        interval === 'hour' ? 'Busiest hour' : 'Busiest day',
        `${(interval === 'hour' ? DAY_HOUR : DAY_SHORT).format(busiest.date)} · ${formatNumber(most)}`,
      ]);
    }
    if (peak > 0) rows.push(['Peak queued', formatNumber(peak)]);
    if (reached > 0) rows.push(['At capacity', formatPercent(reached / samples, 1)]);
    return rows;
  });

  /** Whether there is a column beside the grid at all, or only the key under it. */
  const hasAside = $derived(caption !== undefined || summary.length > 0);

  const legend = $derived.by(() => {
    switch (mode) {
      case 'queue':
        return { ramp: 'pending' as const, less: 'Fewer queued', more: 'More queued' };
      case 'runtime':
        return { ramp: 'busy' as const, less: 'Less runner time', more: 'More' };
      case 'capacity':
        return { ramp: 'pending' as const, less: 'Rarely at capacity', more: 'Mostly' };
      default:
        return { ramp: 'idle' as const, less: 'Fewer finished', more: 'More' };
    }
  });
</script>

<div
  class="matrix {className}"
  bind:this={root}
  data-mode={mode}
  data-interval={interval}
  data-size={size}
  data-aside={hasAside}
  data-beside={roomy}
  style:--room={fills && room !== null ? `${room}px` : null}
  style:--fit={fit !== null ? `${fit}px` : null}
  style:--lead={lead !== null ? `${lead}px` : null}
  style:--slices={layout.slices}
>
  <div class="band" bind:this={band} bind:clientWidth={bandWidth}>
    <div class="frame">
      {#if cells.length === 0}
        <p class="empty">No history in this window yet.</p>
      {:else}
        <div
          class="labels"
          bind:this={labelRow}
          style:--columns={layout.labels.length}
          aria-hidden="true"
        >
          {#each layout.labels as label, c (c)}
            {#if label}<span style:grid-column={c + 2}>{label}</span>{/if}
          {/each}
        </div>
        <div
          class="grid"
          role="grid"
          aria-label={gridLabel}
          bind:this={grid}
          style:--columns={layout.labels.length}
        >
          {#each layout.rows as row, r (r)}
            <div role="row" class="row">
              <!-- Every row is named for assistive technology; only every
                   other weekday is drawn, as a contribution graph does it,
                   because seven names in ten-pixel rows set as a smear. -->
              <span role="rowheader" class="rowhead" aria-label={row.header}
                >{row.shown ? row.header : ''}</span
              >
              <!-- A column's slices of one day, side by side in one track: the
                   gap between days is the grid's, the one within a day the
                   slot's, and a day reads as one thing rather than as squares
                   that happen to be neighbours. -->
              {#each slotsOf(row.cells) as slot, c (c)}
                <div class="slot">
                  {#each slot as cell, k (k)}
                    {#if cell}
                      {@const look = paint(cell.bucket, mode, scale)}
                      <div
                        role="gridcell"
                        class="sq cell"
                        tabindex={focusIndex === cell.index ? 0 : -1}
                        data-index={cell.index}
                        data-kind={look.kind}
                        data-tone={look.tone}
                        data-level={look.level}
                        aria-label={describe(cell.bucket, cell.date, interval)}
                        aria-selected={selected === cell.index}
                        onmouseenter={(e) =>
                          enter(e.currentTarget, cell.bucket, cell.date, interval, cell.index)}
                        onmouseleave={(e) => leave(e.currentTarget)}
                        onfocus={(e) =>
                          enter(e.currentTarget, cell.bucket, cell.date, interval, cell.index)}
                        onblur={(e) => blur(e.currentTarget)}
                        onclick={() => {
                          focusIndex = cell.index;
                          select(cell.index);
                        }}
                        onkeydown={(e) => onKeydown(e, cell)}
                      ></div>
                    {:else}
                      <span class="blank" role="presentation"></span>
                    {/if}
                  {/each}
                </div>
              {/each}
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <div class="aside" bind:this={aside}>
      {#if caption}<div class="caption">{@render caption()}</div>{/if}
      {#if summary.length}
        <dl class="figures window">
          {#each summary as [label, value] (label)}
            <div>
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          {/each}
        </dl>
      {/if}
    </div>

    <div class="legend">
      <span class="ramp">
        <span>{legend.less}</span>
        <i class="sq" data-kind="quiet" aria-hidden="true"></i>
        {#each [1, 2, 3, 4] as l (l)}
          <i class="sq" data-kind="active" data-tone={legend.ramp} data-level={l} aria-hidden="true"
          ></i>
        {/each}
        <span>{legend.more}</span>
      </span>
      {#if mode === 'outcomes'}
        <span class="ramp">
          <span>Failures, few</span>
          {#each [1, 2, 3, 4] as l (l)}
            <i class="sq" data-kind="failing" data-tone="danger" data-level={l} aria-hidden="true"
            ></i>
          {/each}
          <span>to most</span>
        </span>
        <span class="ramp">
          <i class="sq" data-kind="waiting" data-tone="pending" data-level="3" aria-hidden="true"
          ></i>
          <span>Queued, none finished</span>
        </span>
      {:else if mode === 'capacity'}
        <span class="ramp">
          <i class="sq" data-kind="unsampled" aria-hidden="true"></i>
          <span>Not observed</span>
        </span>
      {/if}
    </div>
  </div>

  <!-- Not drawn: a square held at its ceiling, so the layout above can read
       the ceiling back in pixels rather than repeating the token file. -->
  <span class="ceiling" bind:this={ceiling} aria-hidden="true"></span>

  {#if chosen}
    {@const range = rangeOf(chosen)}
    {@const split = OUTCOMES.filter((o) => chosen.bucket[o.key] > 0)}
    <section class="detail" aria-label="Selected {intervalNoun(interval)}">
      <header>
        <div class="heading" aria-live="polite">
          <h3>{intervalName(chosen.date, interval)}</h3>
          <p>{headline(chosen.bucket)}</p>
        </div>
        <IconButton
          icon={X}
          label="Close the selected {intervalNoun(interval)}"
          size="sm"
          onclick={() => select(null)}
        />
      </header>
      {#if split.length}
        <div
          class="split"
          role="img"
          aria-label={split
            .map((o) => `${o.label}: ${formatNumber(chosen.bucket[o.key])}`)
            .join(', ')}
        >
          {#each split as o (o.key)}
            <span data-tone={o.tone} style:flex={chosen.bucket[o.key]}></span>
          {/each}
        </div>
        <ul class="outcomes">
          {#each OUTCOMES as o (o.key)}
            <li>
              <i data-tone={o.tone} aria-hidden="true"></i>{o.label}
              <strong>{formatNumber(chosen.bucket[o.key])}</strong>
            </li>
          {/each}
        </ul>
      {/if}
      <dl class="figures">
        {#each figures(chosen.bucket) as [label, value] (label)}
          <div>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        {/each}
      </dl>
      {#if hourly && interval !== 'hour'}
        <div class="hours-wrap">
          {#if hoursShown?.buckets}
            <div class="hours" role="img" aria-label={hoursSummary}>
              {#each hoursShown.buckets as h (h.from)}
                {@const done = completedIn(h)}
                {@const at = new Date(h.from)}
                {@const goesSomewhere = Boolean(links) && (done > 0 || h.queued > 0)}
                <span
                  class="hour"
                  class:clickable={goesSomewhere}
                  data-outside={outside(at) || null}
                  role="presentation"
                  title={describe(h, at, 'hour')}
                  onmouseenter={(e) => enter(e.currentTarget, h, at, 'hour')}
                  onmouseleave={(e) => leave(e.currentTarget)}
                  onclick={goesSomewhere ? (e) => clickHour(e, h, at) : undefined}
                >
                  {#if done > 0}
                    <span class="stack" style:height="{(100 * done) / hourPeak}%">
                      {#each OUTCOMES as o (o.key)}
                        {#if h[o.key] > 0}<i data-tone={o.tone} style:flex={h[o.key]}></i>{/if}
                      {/each}
                    </span>
                  {:else if h.queued > 0}
                    <span class="stack waiting" style:height="{(100 * h.queued) / hourPeak}%"
                    ></span>
                  {/if}
                </span>
              {/each}
            </div>
            <div class="axis" aria-hidden="true">
              {#each hourLabels as label, h (h)}
                {#if label}<span style:left="{(100 * h) / 24}%">{label}</span>{/if}
              {/each}
            </div>
          {:else if hoursShown?.failed}
            <p class="hours-note">
              The hours could not be loaded.
              <button type="button" onclick={() => (hoursAttempt += 1)}>Try again</button>
            </p>
          {:else}
            <div class="hours skeleton" aria-hidden="true">
              {#each Array.from({ length: 24 }, (_, i) => i) as i (i)}
                <span class="hour"><Skeleton width="100%" height="{30 + ((i * 37) % 60)}%" /></span>
              {/each}
            </div>
            <p class="sr-only">Loading the hours.</p>
          {/if}
        </div>
      {/if}
      {#if links}
        {@const out = links(range)}
        {#if out.length}
          <p class="links">
            {#each out as link, i (link.href)}
              {#if i > 0}<span aria-hidden="true">·</span>{/if}<a href={link.href}>{link.label}</a>
            {/each}
          </p>
        {/if}
      {/if}
    </section>
  {/if}

  {#if tip}
    {@const b = tip.bucket}
    {@const split = OUTCOMES.filter((o) => b[o.key] > 0)}
    <div bind:this={tipEl} class="tip" popover="manual" role="presentation" aria-hidden="true">
      <p class="tip-title">{intervalName(tip.at, tip.interval)}</p>
      <p class="tip-head">{headline(b)}</p>
      {#if split.length}
        <div class="split">
          {#each split as o (o.key)}<span data-tone={o.tone} style:flex={b[o.key]}></span>{/each}
        </div>
        <ul class="outcomes">
          {#each split as o (o.key)}
            <li><i data-tone={o.tone}></i>{o.label} <strong>{formatNumber(b[o.key])}</strong></li>
          {/each}
        </ul>
      {/if}
      <dl class="figures">
        {#each figures(b) as [label, value] (label)}
          <div>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        {/each}
      </dl>
      {#if tip.interval === interval}
        <p class="tip-hint">
          {tip.index !== null && tip.index === selected
            ? 'Selected'
            : hourly && interval !== 'hour'
              ? 'Select for the hours and the jobs'
              : 'Select for the jobs'}
        </p>
      {:else if tip.interval === 'hour' && links}
        <p class="tip-hint">
          {b.failed > 0
            ? 'Click the red for its failed jobs, elsewhere for the day'
            : 'Click for the day'}
        </p>
      {/if}
    </div>
  {/if}
</div>

<style>
  .matrix {
    /* The square and its gap are the only measures here; every other size is
       read off the rendered grid, so this pair is the whole geometry. Ten
       pixels is the square a contribution graph taught everyone to read, and
       a one-off measure of this component's own layout rather than a token. */
    --cell-base: 10px;
    /* Twice the size asked for, and no further: past that a square stops
       reading as one square among many and starts reading as a tile. */
    --cell-max: var(--z-space-5);
    --cell: var(--cell-base);
    --gap: var(--z-nudge-3);
    /* The floor a square shrinks to so the whole window fits the width, and
       no further: below it a square is a speck, and the frame scrolls. */
    --cell-min: var(--z-nudge-3);
    /* Between the slices of one day: close enough to read as one day, apart
       enough to read as several squares. */
    --inner: var(--z-nudge-1);
    min-width: 0;
  }
  .ceiling {
    position: fixed;
    top: 0;
    left: 0;
    width: var(--cell-max);
    height: 0;
    visibility: hidden;
    pointer-events: none;
  }
  .matrix[data-size='md'] {
    --cell-base: var(--z-space-3);
    --cell-max: var(--z-space-6);
  }
  .matrix[data-size='lg'] {
    --cell-base: var(--z-space-4);
    --cell-max: var(--z-space-8);
    --gap: var(--z-space-1);
  }
  /* The square grows into the room measured for one column, within the pair
     above, and where even the size asked for would not fit, comes down to
     the largest square that does -- never past the floor. Redefined on the
     grid rather than on the matrix so the key's own squares stay the size the
     key is read at. A column is a day's slices side by side. */
  .labels,
  .grid {
    --cell: clamp(
      var(--cell-min),
      max(var(--room, 0px), min(var(--cell-base), var(--fit, var(--cell-base)))),
      var(--cell-max)
    );
    --slot: calc(var(--slices, 1) * var(--cell) + (var(--slices, 1) - 1) * var(--inner));
  }
  /*
    The grid, and the aside beside it. Two tracks rather than two flex items,
    so the split needs no arbiter: the grid takes the width it has actually
    grown to and the aside takes the rest, down to a column of its own. A
    range whose square hits its ceiling -- a quarter is thirteen week columns
    however large the square -- therefore hands the difference to the figures
    rather than leaving it white.
  */
  .band {
    display: grid;
    grid-template-columns: minmax(0, max-content) minmax(var(--aside), 1fr);
    align-items: stretch;
    gap: var(--z-space-2) var(--z-space-6);
    /* One column of figures: the aside's least width, and the measure both
       budgets above are taken from. Named once here, used by the track and by
       the aside, and read back in pixels off the rendered aside. */
    --aside: 11rem;
  }
  .frame {
    /* A day of hours is one row of squares against an aside several lines
       tall. Centred, that is a band with a strip through the middle of it;
       aligned to the top it is a strip with a hole under it. Every taller
       grid sets the height itself, so this does nothing to those. */
    align-self: center;
    min-width: 0;
    overflow-x: auto;
    overflow-y: hidden;
    /* Room for the selection ring on the bottom row. */
    padding: var(--z-nudge-3) var(--z-nudge-3) var(--z-nudge-3) 0;
  }
  .empty {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .labels,
  .grid {
    display: grid;
    grid-template-columns: max-content repeat(var(--columns), var(--slot));
    column-gap: var(--gap);
    width: max-content;
  }
  /* The labels have no row headings of their own, so their first track is
     the grid's, measured: without it every label sat one heading's width to
     the left of the column it names. */
  .labels {
    grid-template-columns: var(--lead, 0px) repeat(var(--columns), var(--slot));
    grid-template-rows: var(--z-leading-2xs);
    margin-bottom: var(--z-nudge-2);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
  }
  .labels span {
    grid-row: 1;
    white-space: nowrap;
  }
  /* A month that starts in the newest column has a column's width to be named
     in, and its name is wider than that: ending the label at the column's edge
     keeps it from overflowing the frame, which would grow a scrollbar for the
     want of a few pixels. */
  .labels span:last-child {
    justify-self: end;
  }
  .grid {
    row-gap: var(--gap);
  }
  .row {
    display: contents;
  }
  .slot {
    display: flex;
    column-gap: var(--inner);
  }
  .rowhead {
    padding-right: var(--z-space-2);
    font-size: var(--z-text-2xs);
    line-height: var(--cell);
    color: var(--z-text-muted);
    white-space: nowrap;
  }
  /* A slot the window does not reach -- the days of the first week before it
     begins, the rest of this week after today, the hours still to come. It
     is drawn as a square rather than left as a hole so every row runs the
     full width and the grid reads as one block; the sunken fill with no edge
     keeps it quieter than a quiet day, which has one, so it never passes for
     a day the fleet sat idle. */
  .blank {
    display: block;
    flex: none;
    width: var(--cell);
    height: var(--cell);
    border-radius: min(var(--z-nudge-2), var(--cell) / 4);
    background: var(--z-surface-sunken);
  }

  /*
    The square. Its darkness is a mix of the status hue into the sunken
    surface, so one rule paints both themes: the hue is the theme's own and
    the surface it is mixed into is the theme's own.
  */
  .sq {
    display: block;
    flex: none;
    width: var(--cell);
    height: var(--cell);
    /* A corner a quarter of the square at most: a year on a phone is squares
       a few pixels across, and the desktop's corner would make them dots. */
    border-radius: min(var(--z-nudge-2), var(--cell) / 4);
    background: var(--z-surface-sunken);
    box-shadow: inset 0 0 0 var(--z-border-width) var(--z-border);
    --tone: var(--z-idle);
    --mix: 0%;
    --fill: color-mix(in srgb, var(--tone) var(--mix), var(--z-surface-sunken));
  }
  .sq[data-tone='idle'] {
    --tone: var(--z-idle);
  }
  .sq[data-tone='danger'] {
    --tone: var(--z-danger);
  }
  .sq[data-tone='pending'] {
    --tone: var(--z-pending);
  }
  .sq[data-tone='busy'] {
    --tone: var(--z-busy);
  }
  .sq[data-level='1'] {
    --mix: 35%;
  }
  .sq[data-level='2'] {
    --mix: 55%;
  }
  .sq[data-level='3'] {
    --mix: 78%;
  }
  .sq[data-level='4'] {
    --mix: 100%;
  }
  .sq[data-kind='healthy'],
  .sq[data-kind='active'] {
    background: var(--fill);
    box-shadow: none;
  }
  /* A hole in the middle: the one shape a failure has at this size. A fifth
     of the square across at most, so a square shrunk to fit is still mostly
     red rather than mostly hole. */
  .sq[data-kind='failing'] {
    --hole: min(var(--z-nudge-2), var(--cell) / 5);
    background: radial-gradient(
      circle,
      var(--z-surface) 0 var(--hole),
      var(--fill) calc(var(--hole) + var(--z-nudge-1))
    );
    box-shadow: none;
  }
  /* Hollow: work is waiting and nothing has finished. */
  .sq[data-kind='waiting'] {
    background: none;
    box-shadow: inset 0 0 0 var(--z-border-width-thick) var(--fill);
  }
  /* Dashed: nobody was looking. */
  .sq[data-kind='unsampled'] {
    background: none;
    box-shadow: inset 0 0 0 var(--z-border-width) var(--z-border-strong);
    background-image: repeating-linear-gradient(
      45deg,
      transparent 0 var(--z-nudge-2),
      var(--z-border-strong) var(--z-nudge-2) var(--z-nudge-3)
    );
  }
  .cell {
    cursor: pointer;
    transition:
      background-color var(--z-motion-base) var(--z-ease),
      box-shadow var(--z-motion-fast) var(--z-ease);
  }
  .cell:hover {
    outline: var(--z-border-width) solid var(--z-text-subtle);
    outline-offset: 0;
  }
  .cell[aria-selected='true'] {
    outline: var(--z-border-width-thick) solid var(--z-accent);
    outline-offset: var(--z-nudge-1);
  }
  .cell:focus-visible {
    border-radius: var(--z-nudge-2);
  }

  .aside {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: var(--z-space-2);
    min-width: var(--aside);
  }
  /* Nothing to say beside the grid -- the Usage page's matrix before its first
     job -- and the grid has the band to itself. */
  .matrix[data-aside='false'] .band {
    grid-template-columns: minmax(0, 1fr);
  }
  .matrix[data-aside='false'] .aside {
    display: none;
  }
  /*
    The key runs under both, the whole width of the band. It explains the
    squares, so it belongs beneath them rather than in a column of its own,
    and the full width is what keeps it to one line -- three ramps stacked
    beside the grid were three lines the band could not afford.
  */
  .legend {
    grid-column: 1 / -1;
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-4);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
  }
  /* The window's figures, laid out as a square's are, one column where the
     aside is a column and a row of them where it is a row. */
  .figures.window {
    margin: 0;
    row-gap: var(--z-space-1);
    grid-template-columns: repeat(auto-fit, minmax(var(--aside), 1fr));
  }
  /*
    A narrow band cannot always have both, and which one gives way is measured
    rather than assumed: a grid that would use the whole band -- a week of
    hours, a year of days -- takes it, and the figures go under; a grid that
    would stop short of it -- a month is five week columns whatever the square
    -- keeps them beside, where the width it does not want is of some use.
    Stacking either way round would leave one of the two as a stub against a
    field of white, which is the thing a band of squares can least afford.
  */
  @media (max-width: 768px) {
    /* The smallest square, whatever the range: a day of hours is twenty-four
       columns and they have to fit between the gutters. The ceiling is left
       where it is, so a grid with more room than that still grows into it --
       a week of hours at ten pixels is half a band. */
    .matrix,
    .matrix[data-size='md'],
    .matrix[data-size='lg'] {
      --cell-base: 10px;
      --gap: var(--z-nudge-2);
    }
    /* A column of figures still reads at the width it does on a desktop, so
       what gives between them here is the space, not the column. */
    .band {
      column-gap: var(--z-space-4);
    }
    /* One track, so the aside goes under the grid rather than beside it.
       --aside stays what it is either way, because the figures lay themselves
       out a column of that width at a time however the band is split. */
    .matrix[data-beside='false'] .band {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .ramp {
    display: inline-flex;
    align-items: center;
    gap: var(--z-nudge-3);
  }
  .ramp > span {
    padding: 0 var(--z-nudge-2);
  }
  .caption {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }

  /* -- the detail ------------------------------------------------------- */
  .detail {
    margin-top: var(--z-space-4);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .detail header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  .heading h3 {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
  }
  .heading p {
    margin: var(--z-nudge-2) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }

  .split {
    display: flex;
    gap: var(--z-border-width);
    height: var(--z-space-2);
    margin-top: var(--z-space-3);
    border-radius: var(--z-radius-full);
    overflow: hidden;
    background: var(--z-border);
  }
  .split span {
    min-width: var(--z-nudge-2);
  }
  /* The outcome segments of a bar and the dots of a key -- and only those.
     The squares carry the same attribute and paint themselves. */
  .split [data-tone='idle'],
  .outcomes [data-tone='idle'],
  .stack [data-tone='idle'] {
    background: var(--z-idle);
  }
  .split [data-tone='danger'],
  .outcomes [data-tone='danger'],
  .stack [data-tone='danger'] {
    background: var(--z-danger);
  }
  .split [data-tone='neutral'],
  .outcomes [data-tone='neutral'],
  .stack [data-tone='neutral'] {
    background: var(--z-neutral);
  }
  .split [data-tone='pending'],
  .outcomes [data-tone='pending'],
  .stack [data-tone='pending'] {
    background: var(--z-pending);
  }
  .outcomes {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-4);
    margin: var(--z-space-2) 0 0;
    padding: 0;
    list-style: none;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .outcomes li {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
  }
  .outcomes i {
    width: var(--z-space-2);
    height: var(--z-space-2);
    border-radius: var(--z-radius-full);
  }
  .outcomes strong,
  .figures dd {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
    font-variant-numeric: tabular-nums;
  }
  .figures {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(7.5rem, 1fr));
    gap: var(--z-space-2) var(--z-space-4);
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-xs);
  }
  .figures div {
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .figures dt {
    color: var(--z-text-muted);
  }
  .figures dd {
    margin: 0;
    white-space: nowrap;
  }

  .hours-wrap {
    margin-top: var(--z-space-4);
  }
  .hours {
    display: flex;
    align-items: flex-end;
    gap: var(--z-nudge-2);
    height: var(--z-space-16);
    padding-bottom: var(--z-nudge-1);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .hour {
    flex: 1;
    display: flex;
    align-items: flex-end;
    height: 100%;
    min-width: 0;
    border-radius: var(--z-nudge-2) var(--z-nudge-2) 0 0;
  }
  .hour:hover {
    background: var(--z-surface-hover);
  }
  .hour.clickable {
    cursor: pointer;
  }
  /* The hours of the day around a selected slice of it: there for the
     shape of the day, stepped back so the slice's own hours lead. */
  .hour[data-outside] {
    opacity: 0.4;
  }
  .stack {
    display: flex;
    flex-direction: column-reverse;
    width: 100%;
    min-height: var(--z-nudge-2);
    border-radius: var(--z-nudge-2) var(--z-nudge-2) 0 0;
    overflow: hidden;
  }
  .stack i {
    display: block;
    min-height: var(--z-nudge-1);
  }
  .stack.waiting {
    border: var(--z-border-width-thick) solid var(--z-pending);
    border-bottom: 0;
  }
  .hours.skeleton .hour {
    align-items: flex-end;
  }
  .axis {
    position: relative;
    height: var(--z-leading-2xs);
    margin-top: var(--z-nudge-2);
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
  }
  .axis span {
    position: absolute;
    top: 0;
  }
  .hours-note {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .hours-note button {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    text-decoration: underline;
    cursor: pointer;
  }
  .links {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-xs);
  }
  .links a {
    color: var(--z-accent);
  }
  .links span {
    color: var(--z-text-subtle);
  }

  /* -- the tooltip ------------------------------------------------------ */
  .tip {
    position: fixed;
    inset: auto;
    margin: 0;
    z-index: var(--z-layer-dropdown);
    width: max-content;
    max-width: min(300px, calc(100vw - var(--z-space-4)));
    max-height: calc(100dvh - var(--z-space-4));
    overflow-y: auto;
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-raised);
    color: var(--z-text);
    box-shadow: var(--z-shadow-md);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    pointer-events: none;
  }
  .tip-title {
    margin: 0;
    font-weight: var(--z-weight-semibold);
  }
  .tip-head {
    margin: var(--z-nudge-2) 0 0;
    color: var(--z-text-muted);
  }
  .tip .split {
    margin-top: var(--z-space-2);
  }
  .tip .figures {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .tip-hint {
    margin: var(--z-space-2) 0 0;
    color: var(--z-text-subtle);
  }
</style>
