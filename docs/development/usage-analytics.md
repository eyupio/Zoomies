---
# Written for contributors, not for someone choosing a runner controller:
# published so links to it work, kept out of search results and the sitemap.
noindex: true
---

# Usage analytics

The Usage page combines summary metrics, activity and runner-hour trends, the
activity matrix, ranked execution consumption, and the detailed CSV report. Pool, host, repository, workflow and installation groupings share the
same range, to the minute. Selecting a ranked group or the focus selector narrows every
visual and the CSV export. Range (a quick `range=` such as `6h` or `30d`, or `since`/`until` typed by hand), grouping and focused group are shareable URLs.
Hosts also exposes fleet summary metrics and links into a host's usage report.

## Reporting semantics

- Queued, started and completed counts belong to the interval in which each
  event occurred. Completed outcomes distinguish success, failure (including
  runner faults), cancelled/skipped and unknown. Success rate divides successes
  by **all** completions, including unknown outcomes.
- Execution and allocated runner time are clipped at the interval edges and the
  observation time. Ongoing jobs contribute execution and peak concurrency.
- Queue wait is weighted by jobs started, not by the number of groups. Busy
  share divides execution time by allocation; absent allocation is not zero.
- Idle runner allocation belongs to pools, hosts and installations. Repository
  and workflow reports cannot attribute it, and hide allocation and costs.
  Host assignment comes from the retained runner record; missing assignments
  remain unattributed. Purged records cannot be reconstructed.
- History uses elapsed-hour buckets for windows of at most 48 hours, otherwise
  elapsed-day buckets anchored at the requested start. These are fixed elapsed
  intervals, not local calendar days across daylight-saving transitions.
- Capacity history is collected from scheduler host-capacity placement blocks.
  It is **not** inferred from queue size, runner utilisation or today's limits.
  One row per pool/minute records whether any observed reconcile was blocked.
  Multiple blocked passes are one blocked pool-minute, not multiple incidents.
  Missing samples remain visibly unsampled. This does not report exact incident
  durations, pool max-runner limits or repository quotas as host-capacity blocks.
- Capacity history starts when this version runs, follows job retention, and is
  not backfilled. Job history likewise reflects retained jobs and runners. Costs
  remain estimates using administrator-configured pool rates.

## The activity matrix

`lib/insights/ActivityMatrix.svelte` draws a series of usage buckets as a
contribution graph, and the Overview and the Usage page both use it. Daily
buckets become a calendar, a column per week and a row per weekday; hourly
buckets become a row per day and a column per hour. Buckets of a few hours
(the Overview's month in four-hour buckets and quarter in eight) become the
calendar again, with each day's buckets side by side in its weekday's row, so
a column is a week of days each several squares wide. The Usage page draws
whatever range it is given in the squares the Overview uses for a window as
long (`matrixInterval` in `activity.ts`, the narrowest of hours, four hours
and eight that the API will cut the whole window into, and days beyond) so a
month there fills the panel the way the Overview's does. Where those squares
are narrower than the report's own (hourly up to 48 hours, daily beyond), the
matrix asks for its own series in the report's grouping and focus, and a
square chosen in it moves the chart's crosshair to the interval it falls in
rather than to the same index.

The band beside the grid is the window in words: the totals the caller gives
it, then the figures the component works out from the squares on screen --
failure rate, execution time, the busiest interval, the deepest queue, and the
capacity share once a ceiling has actually been reached. Which of the two
takes the width is settled by shape rather than by a breakpoint. A grid with
at least as many columns as rows grows its square, up to twice the size the
caller asked for, until it fills the room left once the aside is owed two
columns of figures; a grid with more rows than columns (a few weeks of whole
days, which is five week columns whatever the square) would reach the foot
of the panel long before the right of it, so it keeps its size and the aside
takes the width. Any grid that would not fit at the size asked for shrinks instead, to
the largest square that fits beside a single column of figures, down to a
floor below which the frame scrolls: a window is never cut to fit, because a
year that does not fit is a year with months missing. Where the columns end
up too close for a month's short name, the labels take its initial.

The arithmetic is in `lib/insights/activity.ts` and is tested in Node: how a
square is painted under each mode, how the darkness steps are cut (quarters of
the busiest square on screen for counts; fixed bands for a failure share and
for a capacity share, so half of everything failing looks the same on a good
week as on a bad one), how a calendar is laid out across a daylight-saving
change, and how a `job.updated` frame is folded into the square its moment
belongs to. The Overview fetches a year once and folds frames into today;
runner time is not folded, because a frame cannot say which hour a job's
running fell in, and the next fetch carries the exact figure.

Selecting a day asks for that day again with hourly buckets, in the same
grouping and focus the page is showing, and draws the hours under the grid
with links to the Jobs page cut to the day. The links carry calendar dates
rather than instants because that is what the Jobs page filters by.

The Overview's quick ranges ask for the bucket width outright: `interval=hour`
for today and the last week, so a week is a week of hours and never a week of
days, `interval=4h` for the month, `interval=8h` for the quarter, and
`interval=day` for the year. The route takes any whole number of hours a day
divides into, so a day is always a whole number of squares, and refuses more
than 336 buckets a row narrower than a day (14 days of hours, 56 of four
hours, 112 of eight; a year of hours, times every repository, is a payload
nobody wants) and says so with what to ask for instead.

## Extending the visual system

`MetricGrid.svelte` provides responsive semantic definition lists and fixed
status tones. `ChartPanel.svelte` provides consistent chart framing, descriptions
and optional actions. Both consume the existing design tokens, supporting light
and dark themes without new palette values. Usage-specific aggregation and
visuals live in `lib/usage/UsageInsights.svelte`, and the matrix they share with
the Overview in `lib/insights/`; keep domain calculations separate when adding
host resources, pool health or repository analytics elsewhere.

The API adds outcome counts and bounded history to each usage row, a `host`
grouping, and an exact `key` filter for JSON and CSV. The original detail columns
and CSV column order are preserved. Capacity is aggregated in SQLite before
crossing into the application, and a per-minute upsert avoids growing a row for
every scheduling pass. Read-only analytics does not change scheduler placement.
