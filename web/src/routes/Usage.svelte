<script lang="ts">
  import { onMount } from 'svelte';
  import PageHeader from '../lib/components/PageHeader.svelte';

  type Row = {
    key: string;
    job_execution_seconds: number;
    /**
     * Absent when grouping by repository or workflow: a runner's idle time
     * belongs to neither, and the API leaves the figure out rather than say
     * zero.
     */
    allocated_runner_seconds?: number;
    jobs: number;
    average_queue_wait_seconds: number;
    peak_concurrency: number;
    estimated_cost?: number;
  };
  let items: Row[] = $state([]);
  let error = $state('');
  let loading = $state(true);
  let group = $state('pool');
  // The grouping the rows on screen came from, which is not the select's value
  // once the operator has changed it and not yet applied.
  let shownGroup = $state('pool');
  const attributable = $derived(shownGroup === 'pool' || shownGroup === 'installation');
  const now = new Date(),
    monthAgo = new Date(now.getTime() - 30 * 86400000);
  let from = $state(monthAgo.toISOString().slice(0, 10)),
    to = $state(now.toISOString().slice(0, 10));
  const params = () =>
    new URLSearchParams({
      from: new Date(from + 'T00:00:00Z').toISOString(),
      to: new Date(to + 'T23:59:59Z').toISOString(),
      group_by: group,
    });
  async function load() {
    loading = true;
    error = '';
    try {
      const r = await fetch('/api/v1/usage?' + params());
      if (!r.ok) throw new Error((await r.json()).error?.message ?? 'Could not load usage');
      items = (await r.json()).items;
      shownGroup = group;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }
  const hours = (row: Row) =>
    row.allocated_runner_seconds === undefined
      ? 'Not attributable'
      : (row.allocated_runner_seconds / 3600).toFixed(2);
  const cost = (row: Row) =>
    row.allocated_runner_seconds === undefined
      ? 'Not attributable'
      : row.estimated_cost === undefined
        ? 'Not configured'
        : row.estimated_cost.toFixed(2);
  onMount(load);
</script>

<PageHeader title="Usage" subtitle="Runner capacity and job activity for a bounded date range." />
<form
  onsubmit={(e) => {
    e.preventDefault();
    load();
  }}
>
  <label>From <input type="date" bind:value={from} /></label><label
    >To <input type="date" bind:value={to} /></label
  >
  <label
    >Group by <select bind:value={group}
      ><option value="pool">Pool</option><option value="repository">Repository</option><option
        value="workflow">Workflow</option
      ><option value="installation">Installation</option></select
    ></label
  >
  <button>Apply</button><a class="export" href={'/api/v1/usage.csv?' + params()}>Export CSV</a>
</form>
<p class="note">
  Costs are estimates based only on optional rates assigned by administrators. Zoomies does not
  embed cloud prices.
</p>
{#if loading}<p>Loading usage…</p>{:else if error}<p role="alert">{error}</p>{:else}
  {#if !attributable}
    <p class="note">
      Runner-hours and cost are shown for pools and installations only. A runner's idle time belongs
      to no single {shownGroup}, so they are not attributed here rather than shown as zero.
    </p>
  {/if}
  <div class="table">
    <table>
      <thead
        ><tr
          ><th>{shownGroup}</th><th>Runner-hours</th><th>Jobs queued</th><th>Average queue wait</th
          ><th>Peak concurrency</th><th>Estimated cost</th></tr
        ></thead
      ><tbody>
        {#each items as row (row.key)}<tr
            ><td>{row.key || 'Unknown'}</td><td>{hours(row)}</td><td>{row.jobs}</td><td
              >{row.average_queue_wait_seconds.toFixed(1)}s</td
            ><td>{row.peak_concurrency}</td><td>{cost(row)}</td></tr
          >{/each}
      </tbody>
    </table>
  </div>{/if}

<style>
  form {
    display: flex;
    gap: 1rem;
    align-items: end;
    flex-wrap: wrap;
    margin: 1rem 0;
  }
  label {
    display: grid;
    gap: 0.3rem;
  }
  input,
  select,
  button,
  .export {
    padding: 0.55rem;
    border: 1px solid var(--z-border);
    border-radius: 0.4rem;
    background: var(--z-surface);
    color: inherit;
  }
  .export {
    text-decoration: none;
  }
  .note {
    color: var(--z-text-muted);
  }
  .table {
    overflow: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 0.7rem;
    border-bottom: 1px solid var(--z-border);
  }
  th {
    text-transform: capitalize;
  }
</style>
