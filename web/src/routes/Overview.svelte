<!--
  The Overview: the one page that has to earn a place on a second monitor.

  Reading order is deliberate. The four numbers say what the fleet is doing;
  anything that needs a person comes immediately after them, because a problem
  found at the bottom of a page is a problem found late. Then the two things an
  operator watches when nothing is wrong -- where the capacity is going, and
  what the scheduler decided -- and finally the work itself.

  What needs a person is one line here rather than a full panel. The list it
  summarises lives in the problems drawer, reachable from the top bar on every
  page: a fleet whose deliberate configuration warnings pushed its pools and its
  running jobs below the fold was answering "what is happening right now?" with
  a month-old decision.

  Nothing on this page polls, and nothing on it can be refreshed by hand. The
  fleet cache subscribes to `stats`, `scaling`, `problems.updated`, `runner.*`,
  `pool.*` and `host.*`; the panels below add `job.updated` for the running
  jobs. A reconnect ends in one reconciling fetch, which is the only fetch that
  ever happens twice.
-->
<script lang="ts">
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { fleet } from '$lib/state/fleet.svelte';
  import ActiveJobs from '$lib/overview/ActiveJobs.svelte';
  import FleetMetrics from '$lib/overview/FleetMetrics.svelte';
  import PoolUtilisation from '$lib/overview/PoolUtilisation.svelte';
  import ProblemsSummary from '$lib/overview/ProblemsSummary.svelte';
  import ScalingFeed from '$lib/overview/ScalingFeed.svelte';

  const loading = $derived(!fleet.loaded);
  // A failed reconcile once there is something on screen is not an error state:
  // the stream may well recover, and the last known truth is better than a
  // blank page. Only a first load that never landed gets one.
  const failed = $derived(!fleet.loaded && fleet.error !== null);
</script>

<PageHeader
  title="Overview"
  subtitle="What the fleet is doing right now. Trends and waits cover the last hour."
/>

{#if failed}
  <ErrorState
    error={fleet.error}
    title="The fleet could not be loaded"
    onretry={() => void fleet.reconcile()}
  />
{:else}
  <div class="stack">
    <FleetMetrics {loading} />
    <ProblemsSummary {loading} />
    <div class="split">
      <PoolUtilisation {loading} />
      <ScalingFeed {loading} />
    </div>
    <ActiveJobs />
  </div>
{/if}

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
  }
  .split {
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
    gap: var(--z-space-6);
    align-items: start;
  }
  @media (max-width: 1180px) {
    .split {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--z-space-4);
    }
    .stack {
      gap: var(--z-space-4);
    }
  }
</style>
