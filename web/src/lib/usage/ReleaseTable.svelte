<!--
  Usage's "By release" table: how the jobs of the range fared under each
  controller version, which is the question an operator has after an upgrade --
  did builds get faster, and did fewer of them break.

  It is /jobs/stats grouped by controller version over the report's own range,
  so it counts what the table above it counts and the two cannot disagree about
  the window. Jobs that were never stamped with a release -- recorded before the
  field existed, or never claimed by a pool here -- are one row called
  "unknown", said in words and not left out: dropping them would make the newest
  releases look like the whole of the fleet's history.

  A figure is "--" where no job in the group could be measured, which is not the
  same statement as 0ms. Duration leaves out cancelled and skipped jobs, and the
  note says so, because the API says so and the numbers get quoted without it.
-->
<script lang="ts">
  import { getJobStats } from '$lib/api/client';
  import type { JobStats, JobStatsGroup } from '$lib/api/types';
  import { formatDuration, formatNumber, formatPercent } from '$lib/format';
  import { sentence } from '$lib/errors';

  interface Props {
    /** The report's range, as instants. */
    from: string;
    to: string;
  }
  let { from, to }: Props = $props();

  let stats = $state.raw<JobStats | null>(null);
  let error = $state<string | null>(null);
  let loading = $state(true);

  $effect(() => {
    const controller = new AbortController();
    loading = true;
    error = null;
    // A range typed backwards is the page's own warning to give; asking the
    // server about it would only turn that into a second, worse message.
    if (!from || !to || from > to) {
      stats = null;
      loading = false;
      return;
    }
    getJobStats({ group_by: ['controller_version'], since: from, until: to }, controller.signal)
      .then((next) => {
        if (controller.signal.aborted) return;
        stats = next;
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        stats = null;
        // The server's own sentence: a window over its limit names the setting
        // that holds it, which is the one thing this table cannot say itself.
        error = sentence(
          cause instanceof Error && cause.message
            ? cause.message
            : 'Job statistics could not be read',
        );
      })
      .finally(() => {
        if (!controller.signal.aborted) loading = false;
      });
    return () => controller.abort();
  });

  const groups = $derived<JobStatsGroup[]>(stats?.groups ?? []);

  function release(group: JobStatsGroup): string {
    return group.keys?.controller_version ?? 'unknown';
  }
</script>

<section class="releases" aria-labelledby="by-release-heading" data-testid="usage-by-release">
  <div class="heading">
    <h2 id="by-release-heading">By release</h2>
    <span>completed jobs by the controller version that claimed them</span>
  </div>

  {#if error}
    <p class="note" role="alert">{error}</p>
  {:else if loading && !stats}
    <p class="note">Reading job statistics…</p>
  {:else if groups.length === 0}
    <p class="note" data-testid="usage-by-release-empty">
      No job finished in this range, so there is no release to compare. Widen the range.
    </p>
  {:else}
    <div class="frame">
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <table role="table">
        <caption class="sr-only">Completed jobs in the range, by controller version</caption>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <thead role="rowgroup">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <th role="columnheader" scope="col" class="key-header">Release</th>
            <th role="columnheader" scope="col" class="end">Jobs</th>
            <th role="columnheader" scope="col" class="end">Failed</th>
            <th role="columnheader" scope="col" class="end">Lost to the fleet</th>
            <th role="columnheader" scope="col" class="end">Duration p50</th>
            <th role="columnheader" scope="col" class="end">Duration p95</th>
            <th role="columnheader" scope="col" class="end">Queue wait p50</th>
            <th role="columnheader" scope="col" class="end">Queue wait p95</th>
            <th role="columnheader" scope="col" class="end">Startup p50</th>
            <th role="columnheader" scope="col" class="end">Startup p95</th>
          </tr>
        </thead>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tbody role="rowgroup">
          {#each groups as group (release(group))}
            {@const name = release(group)}
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row" data-release={name}>
              <th role="rowheader" scope="row" class="key" title={name}>
                <span class="name" class:muted={name === 'unknown'}>{name}</span>
                {#if name === 'unknown'}
                  <span
                    class="tag"
                    title="Recorded before releases were stamped, or claimed by no pool here"
                    >not recorded</span
                  >
                {/if}
              </th>
              <td role="cell" class="end tabular" data-label="Jobs">{formatNumber(group.count)}</td>
              <td role="cell" class="end tabular" data-label="Failed"
                >{formatNumber(group.failed)}</td
              >
              <td role="cell" class="end tabular" data-label="Lost to the fleet"
                >{formatNumber(group.fleet_failed)}
                <span class="muted">({formatPercent(group.fleet_failure_rate, 1)})</span></td
              >
              <td role="cell" class="end tabular" data-label="Duration p50"
                >{formatDuration(group.duration.p50_ms)}</td
              >
              <td role="cell" class="end tabular" data-label="Duration p95"
                >{formatDuration(group.duration.p95_ms)}</td
              >
              <td role="cell" class="end tabular" data-label="Queue wait p50"
                >{formatDuration(group.queue_wait.p50_ms)}</td
              >
              <td role="cell" class="end tabular" data-label="Queue wait p95"
                >{formatDuration(group.queue_wait.p95_ms)}</td
              >
              <td role="cell" class="end tabular" data-label="Startup p50"
                >{formatDuration(group.startup.p50_ms)}</td
              >
              <td role="cell" class="end tabular" data-label="Startup p95"
                >{formatDuration(group.startup.p95_ms)}</td
              >
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="note">
      Duration leaves out cancelled and skipped jobs. Startup is measured from the runner's own
      record, which is kept for less time than the job, so older jobs show "--". Release rows are in
      the order each was first seen running.
      {#if stats?.truncated}
        More releases matched than are shown; narrow the range.
      {/if}
    </p>
  {/if}
</section>

<style>
  .releases {
    margin: var(--z-space-5) 0 0;
  }
  .heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-3);
  }
  .heading h2 {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    margin: 0;
  }
  .heading span {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .note {
    max-width: 70ch;
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .frame {
    overflow-x: auto;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  table {
    width: 100%;
    table-layout: fixed;
    border-collapse: collapse;
    font-size: var(--z-text-sm);
  }
  th,
  td {
    padding: var(--z-space-3) var(--z-space-4);
    text-align: left;
    white-space: normal;
    overflow: hidden;
    text-overflow: ellipsis;
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .key-header,
  th.key {
    width: 18%;
  }
  th.key {
    white-space: nowrap;
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  tbody tr:last-child th,
  tbody tr:last-child td {
    border-bottom: 0;
  }
  thead th {
    overflow-wrap: anywhere;
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
    background: var(--z-surface-sunken);
  }
  .end {
    text-align: right;
  }
  .name.muted,
  .muted {
    color: var(--z-text-muted);
    font-weight: var(--z-weight-normal);
  }
  .tag {
    margin-left: var(--z-space-2);
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
    white-space: nowrap;
  }
  .tabular {
    font-variant-numeric: tabular-nums;
  }

  /* Ten columns read down, as the usage table above does, for the same reason:
     a figure squeezed to fit is the wrong figure. */
  @media (max-width: 1180px) {
    .frame {
      border: 0;
      background: none;
      overflow-x: visible;
    }
    table {
      display: block;
    }
    thead {
      display: none;
    }
    tbody {
      display: flex;
      flex-direction: column;
      gap: var(--z-space-3);
    }
    tbody tr {
      padding: var(--z-space-3) var(--z-space-4);
      border: var(--z-border-width) solid var(--z-border);
      border-radius: var(--z-radius-md);
      background: var(--z-surface);
    }
    tbody th.key {
      display: block;
      padding: 0 0 var(--z-space-2);
      border-bottom: var(--z-border-width) solid var(--z-border);
      overflow: visible;
      white-space: normal;
      overflow-wrap: anywhere;
    }
    tbody td {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--z-space-4);
      padding: var(--z-space-2) 0;
      border: 0;
      text-align: right;
      white-space: normal;
    }
    tbody td::before {
      content: attr(data-label);
      flex: none;
      font-size: var(--z-text-2xs);
      font-weight: var(--z-weight-medium);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      color: var(--z-text-muted);
      text-align: left;
    }
  }
</style>
