<!--
  The Overview: the one page that has to earn a place on a second monitor.

  Reading order is deliberate. The four numbers say what the fleet is doing;
  anything that needs a person comes immediately after them, because a problem
  found at the bottom of a page is a problem found late. Then the two things an
  operator watches when nothing is wrong -- where the capacity is going, and
  what the scheduler decided -- and finally the work itself.

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
  import FirstRun from '$lib/overview/FirstRun.svelte';
  import FleetMetrics from '$lib/overview/FleetMetrics.svelte';
  import PoolUtilisation from '$lib/overview/PoolUtilisation.svelte';
  import ProblemsPanel from '$lib/overview/ProblemsPanel.svelte';
  import ScalingFeed from '$lib/overview/ScalingFeed.svelte';

  // Raised by the checklist while it is on screen, so the problems panel knows
  // not to also claim that nothing needs attention.
  let setupPending = $state(false);

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
    <!-- Above the metrics on purpose: on a fleet that has never run a job the
         four numbers are all zero, and what the operator needs is the next
         step, not confirmation that nothing is happening. It removes itself
         for good once a job has run here. -->
    <FirstRun onpending={(pending) => (setupPending = pending)} />
    <FleetMetrics {loading} />
    <ProblemsPanel {loading} {setupPending} />
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
