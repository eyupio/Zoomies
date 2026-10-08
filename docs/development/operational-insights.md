---
# Written for contributors, not for someone choosing a runner controller:
# published so links to it work, kept out of search results and the sitemap.
noindex: true
---

# Operational insights across Zoomies

The operator pages share the visual language introduced by Usage, with metrics
and links that explain the work visible on each page.

| Page | Operational context |
| --- | --- |
| Runners | Job queue depth, busy/idle/starting/draining/failed counts, the runner lifecycle drawn as the state machine with live counts and links, provisioning status, oldest queued demand, an inspectable trend over the last hour, six hours or day |
| Queue | Active provisioning demand and ready/expedited/paused/removed composition across matching records, beside the existing bulk controls |
| Pools | Matched queue depth, enabled/disabled totals, pools at their configured ceiling, ranked demand and headroom |
| Pool and runner detail | Pool queue depth, live runner state, headroom, provisioning state and links to the corresponding queue and usage report |
| Jobs | Current queue/running counts, completion outcomes as a bar whose segments are ways into the jobs they count, P95 queue wait and the same fleet trend |
| Hosts | Eligible slot headroom and how many hosts need attention, then a filter row and the cards, with the capacity map folded behind a button until it is asked for (a choice the browser remembers). Opened, the map shows every host's utilisation over the last minute, hour, six hours, day or week, live, measured CPU, memory and load beside committed CPU, memory, runner slots and disk, each as a share of the machine on one 0–100% axis with the band from 85% marked as pressure, and load past the cores drawn in a lane of its own above it. Either every host on one chart, where pointing at a line or its row brings that host forward and steps the rest back, or a chart per host sharing one x axis and one crosshair, which of the two each page opens in is a fleet setting, `ui.capacity_map.hosts_layout` here and `ui.capacity_map.overview_layout` on the Overview, until an operator picks the other on the page; the lead measurement's newest value is written at the end of each line, a headline names the peak in view and the hosts past the pressure line, and each host's row carries meters that read the moment under the crosshair, with links to its controls and usage history |
| Overview | The activity matrix across the top: a year of days as a contribution graph, coloured by outcome, queue depth, runner time or capacity pressure, with a tooltip per square and an hourly breakdown per selected day; then the same host capacity map the Hosts page draws, beside existing fleet, pool, scaling and outcome information |
| Installations | Connection health, dependent pool totals, low API quota count and per-connection quota meters |

## Counting rules

The job queue and the provisioning queue are different measures. A GitHub job
can still be queued after an operator pauses or removes its provisioning demand.
Runner pages show both, using the merged provisioning API from PR #219. Ready
and expedited demand still obey pool priorities, limits, backoff and host fit.
The oldest demand timestamp is from the server's oldest-first query, not the
currently visible runner rows. Removed demand is excluded from that oldest item.

Fleet and pool context on Runners is independent of the runner grid's search,
host and state filters. Pool context follows the pool selector. Jobs summaries
and history follow the Other runners scope, but are intentionally independent
of table filters. These boundaries are stated next to the visuals. Pool demand
on Pools follows that page's search and enabled/disabled filters. Queue status
composition uses the server's counts: other filters apply, but the status filter
does not reduce the breakdown.

No aggregate is calculated from the first page of a runner or job cache. Pool
signals prefer the controller's per-pool stats. A pool at its ceiling with
queued work is a pressure signal, not proof that the scheduler is failing or
that every queued job should provision immediately.

Host free slots include only connected, uncordoned, compatible hosts. Slots do not
promise that a particular platform, backend or resource request fits. CPU and
memory percentages show reserved commitments against allocatable resources,
not measured CPU or memory utilisation. Disk is reported free space. Missing
resource telemetry stays unknown; commitments above 100% retain the exact
percentage even though the visual meter stops at its boundary.

The fleet trend draws every chosen figure at once (queued jobs, running jobs,
idle, busy and live runners) rather than one picked from a dropdown, because
the question the panel exists to answer is whether anything was waiting and
whether anything was free to take it, and that is two lines rather than two
charts. Colour is the status colour the console uses for that state everywhere
else and the stroke says whether the figure counts jobs or runners, so the two
busy figures share a colour on purpose: a running job and the runner running it
should lie on top of one another. Which figures are on the chart, and which
window, are remembered per browser.

It is one-minute points: sixty of them for the last hour, and for the six-hour
and one-day windows the minutes are folded into five- and fifteen-minute
intervals that carry their peak, because a queue that hit twelve for two
minutes is what somebody looking at a day is looking for. Each figure carries
its own peak through a fold, so a folded point is several readings from the
same interval rather than one minute's snapshot. Stored samples and live stats
are coalesced by minute, with the newer input winning. Missing minutes are
gaps, not zeroes, and a folded interval with no observed minute is a gap too.

Intervals where jobs queued with no idle runner to take them are shaded, and
counted in the line above the chart. It is the trend's version of the capacity
map's pressure band: a count of jobs has no 85% to draw a line at, but "work
was waiting and nothing was free" needs no threshold to be worth seeing. It is
judged a minute at a time and folded afterwards, so it survives zooming out,
folding the figures first would compare the deepest queue in a quarter of an
hour with the most idle runners that quarter ever had.

Pointing at a line singles it out, a chip or a legend row does the same, and the
newest value is written at the end of every line so the chart can be read from
its right-hand edge. A tap chooses a moment and dragging scrubs it, the rows
beneath show every figure at that moment, and "Back to now" lets it go; the
slider is the same inspection for a keyboard. History reloads on reconnect and
aborts on unmount. Provisioning
counts use a throttled event-driven refresh and ignore late results after a
scope change. No scheduler behavior is changed by these views.

## Reusable presentation

- `MetricGrid` supports action links and optional progress tracks using semantic
  tones, accessible names and the existing responsive design tokens.
- `StateBreakdown` presents labelled proportional state bands with exact counts
  and shares; a segment with a destination is a link, and hovering one says the
  count, the share and what the state means.
- `LifecycleFlow` draws the runner state machine in the store's own order, each
  step with its count and a link to the runners in it.
- `TrendPlot` draws the fleet trend at the width it has, one unit to a pixel: a
  solid hairline grid on a round axis, the shaded intervals with nothing free,
  emphasis for a figure singled out, the newest value at the end of every line,
  a coverage strip beneath and a slot for the reading. It breaks its line at
  missing samples. Its arithmetic is `signals.ts`, which `npm run test:unit`
  covers.
- `CapacityPlot` draws one plot of the capacity map at the width it has, one
  unit to a pixel: a solid hairline grid, the pressure band, a lane for load
  past the cores, emphasis for a host or measurement singled out, a value at
  the end of every line, the wash under a lone host and a slot for the
  reading. Its geometry is in `hostSeries.ts`, and the round time labels and the
  spreading of the end labels (which the fleet trend wants on the same terms)
  are in `plot.ts`. `npm run test:unit` covers both.
- `ActivityMatrix` draws usage buckets as a contribution graph with one tab
  stop, a tooltip per square and an inline detail per selection; its
  arithmetic is `activity.ts`, which `npm run test:unit` covers. See
  [usage analytics](usage-analytics.md).
- Domain components under `web/src/lib/insights` compose these pieces, keeping
  data interpretation out of generic presentation components.

Run `npm run test:unit` for aggregation and missing-data semantics. The Playwright
`visual-insights.spec.ts` tests both desktop/mobile and light/dark layouts and
writes screenshots to the test output directory. The normal runner, pool, job,
overview and queue suites protect existing actions and filtering.
