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

  On a desktop the pools and the running jobs share the left-hand column and
  the scaling feed takes the right, cut to their height. The feed is the one
  panel whose length says nothing about the fleet -- ten decisions is ten
  decisions whether there is one pool or twenty -- so letting it set the height
  of the row left a fleet with one pool looking at a screen of blank space
  under it. The decisions that do not fit are a scroll away, inside the panel.
  A phone stacks everything, in reading order.

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
  import ProblemsSummary from '$lib/overview/ProblemsSummary.svelte';
  import ScalingFeed from '$lib/overview/ScalingFeed.svelte';

  // Raised by the checklist while it is on screen, so the problems summary
  // knows not to also claim that nothing needs attention.
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
    <ProblemsSummary {loading} {setupPending} />
    <div class="split">
      <PoolUtilisation {loading} />
      <div class="feed">
        <ScalingFeed {loading} />
      </div>
      <ActiveJobs />
    </div>
  </div>
{/if}

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
  }
  /*
    Two rows on the left -- the pools, then the running jobs, placed in source
    order -- and the feed spanning both on the right. The rows are sized by the
    left-hand panels alone: the feed is taken out of flow inside its cell, so
    however many decisions it holds, it never makes the row taller. What it
    gets instead is the height the left column came to, and the panel scrolls
    inside that.
  */
  .split {
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
    gap: var(--z-space-6);
    align-items: start;
  }
  .feed {
    grid-column: 2;
    grid-row: 1 / span 2;
    align-self: stretch;
    position: relative;
    /* Room for the heading and four decisions: a fleet with one pool and
       nothing running still gets a feed rather than a slot. */
    min-height: 24rem;
  }
  .feed > :global(.panel) {
    position: absolute;
    inset: 0 0 auto;
    max-height: 100%;
  }
  @media (max-width: 1180px) {
    .split {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--z-space-4);
    }
    .feed {
      grid-column: auto;
      grid-row: auto;
      align-self: auto;
      position: static;
      min-height: 0;
    }
    .feed > :global(.panel) {
      position: static;
      max-height: none;
    }
    .stack {
      gap: var(--z-space-4);
    }
  }
</style>
