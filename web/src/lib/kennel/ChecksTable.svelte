<!--
  The checks, one row each: what it looks for, how bad it is, and -- when the
  Overview passes counts -- how many repositories have it open.

  The same table says what Kennel Club checks while it is off, with no counts, so
  the page that explains itself and the page that reports are one list and cannot
  disagree about what the checks are.
-->
<script module lang="ts">
  import type { Severity } from '$lib/api/types';

  export interface CheckRow {
    code: string;
    area: string;
    severity: Severity;
    detects: string;
    /** How many repositories have it open. Left out, there is no such column. */
    repositories?: number;
    disabled: boolean;
  }
</script>

<script lang="ts">
  import { severityStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';

  interface Props {
    rows: readonly CheckRow[];
    label: string;
  }

  let { rows, label }: Props = $props();

  const counted = $derived(rows.some((row) => row.repositories !== undefined));
</script>

<div class="frame">
  <!-- svelte-ignore a11y_no_redundant_roles -->
  <table role="table">
    <caption class="sr-only">{label}</caption>
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <thead role="rowgroup">
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <tr role="row">
        <th role="columnheader" scope="col">Check</th>
        <th role="columnheader" scope="col">Usual severity</th>
        {#if counted}<th role="columnheader" scope="col">Repositories</th>{/if}
      </tr>
    </thead>
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <tbody role="rowgroup">
      {#each rows as row (row.code)}
        <tr role="row" class:off={row.disabled}>
          <td role="cell" data-label="Check">
            <div class="check">
              <span class="detects">{row.detects}</span>
              <code class="code">{row.code}</code>
              {#if row.disabled}
                <Badge tone="neutral" label="Turned off" size="sm" dot={false} />
              {/if}
            </div>
          </td>
          <td role="cell" data-label="Usual severity">
            <Badge status={severityStatus(row.severity)} size="sm" />
          </td>
          {#if counted}
            <td role="cell" data-label="Repositories" class="count">
              {#if (row.repositories ?? 0) > 0}
                <a href="/kennel/repositories?code={encodeURIComponent(row.code)}"
                  >{row.repositories}<span class="sr-only"
                    >{row.repositories === 1 ? ' repository has' : ' repositories have'}
                    {row.code} open</span
                  ></a
                >
              {:else}
                <span class="none">0</span>
              {/if}
            </td>
          {/if}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  table {
    width: 100%;
    /* The frame decides the width and the columns divide it, so the list never
       scrolls sideways -- see "Tables fit the window" in the UI guidelines. */
    table-layout: fixed;
    border-collapse: separate;
    border-spacing: 0;
    font-size: var(--z-text-sm);
  }
  th {
    overflow-wrap: anywhere;
    padding: var(--z-space-2) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
  }
  th:first-child {
    width: 60%;
  }
  td {
    overflow-wrap: anywhere;
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text);
    vertical-align: top;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
  tr.off td {
    color: var(--z-text-subtle);
  }
  .check {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-1);
  }
  .detects {
    font-weight: var(--z-weight-medium);
  }
  .code {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .count {
    font-variant-numeric: tabular-nums;
  }
  .none {
    color: var(--z-text-subtle);
  }
  /*
    On a phone each row becomes a card and each cell carries its own heading:
    nothing is dropped and nothing is truncated, and the list reads down instead
    of across.
  */
  @media (max-width: 768px) {
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
      padding: var(--z-space-3);
    }
    tbody tr {
      display: block;
      border: var(--z-border-width) solid var(--z-border);
      border-radius: var(--z-radius-md);
      background: var(--z-surface);
    }
    tbody td {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--z-space-4);
      padding: var(--z-space-2) var(--z-space-3);
      border: 0;
      text-align: right;
      overflow-wrap: anywhere;
    }
    tbody td::before {
      content: attr(data-label);
      flex: none;
      color: var(--z-text-muted);
      font-size: var(--z-text-2xs);
      font-weight: var(--z-weight-medium);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      text-align: left;
    }
    .check {
      align-items: flex-end;
    }
  }
</style>
