<!--
  One pool: what it is set to, what it is running, and why it last changed size.

  Editing opens the same wizard the pool was created with, in place, with `?edit=1`
  in the URL so the browser's Back button leaves the form exactly as an operator
  expects it to. A pool the controller keeps has no wizard to open -- its labels,
  size and limits follow its hosts -- so the same request opens the three
  settings it leaves to an operator instead.
-->
<script lang="ts">
  import RunnerInsights from '$lib/insights/RunnerInsights.svelte';
  import { Download, Gauge, Pencil, Power, PowerOff, Trash2 } from '@lucide/svelte';
  import {
    deletePool,
    disablePool,
    enablePool,
    getAutoPools,
    getPool,
    listJobs,
    listScalingEvents,
    prewarmPool,
  } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { AutoPools, BackendKind, Job, Pool, Problem, ScalingEvent } from '$lib/api/types';
  import { formatNumber, pluralise } from '$lib/format';
  import { router } from '$lib/router';
  import { poolStatus } from '$lib/status';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import UtilisationBar from '$lib/components/UtilisationBar.svelte';
  import PoolAutoDialog from '$lib/pools/PoolAutoDialog.svelte';
  import PoolAutoSummary from '$lib/pools/PoolAutoSummary.svelte';
  import PoolBackendSwitch from '$lib/pools/PoolBackendSwitch.svelte';
  import PoolConfig from '$lib/pools/PoolConfig.svelte';
  import PoolHostSizes from '$lib/pools/PoolHostSizes.svelte';
  import PoolJobs from '$lib/pools/PoolJobs.svelte';
  import PoolRunnerLimitsDialog from '$lib/pools/PoolRunnerLimitsDialog.svelte';
  import PoolRunners from '$lib/pools/PoolRunners.svelte';
  import PoolScaling from '$lib/pools/PoolScaling.svelte';
  import PoolWarnings from '$lib/pools/PoolWarnings.svelte';
  import RunsOnPreview from '$lib/pools/RunsOnPreview.svelte';
  import TestJob from '$lib/pools/TestJob.svelte';
  import PoolWizardForm from '$lib/pools/PoolWizardForm.svelte';
  import { backendLabel } from '$lib/pools/PoolVocabulary.svelte';
  import { isAutomatic } from '$lib/pools/auto';
  import { deletionConsequences } from '$lib/pools/consequences';

  const JOB_LIMIT = 10;
  const SCALING_LIMIT = 20;

  const id = $derived(router.params['id'] ?? '');
  const canOperate = $derived(session.can('operator'));

  /* -- the pool ------------------------------------------------------------- */

  let fetched = $state<Pool | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let attempt = $state(0);

  $effect(() => {
    const poolId = id;
    // Reading the counter is what makes "Try again" fetch again.
    const retry = attempt;
    if (poolId === '' || retry < 0) return;
    const controller = new AbortController();
    loading = true;
    getPool(poolId, controller.signal)
      .then((result) => {
        fetched = result;
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => {
        loading = false;
      });
    return () => controller.abort();
  });

  // The cache is live over SSE, so prefer it and fall back to our own fetch --
  // which is what a deep link into a cold tab actually hits.
  const pool = $derived(fleet.pool(id) ?? fetched);
  // A pool the controller keeps has no wizard: see the note at the top.
  const automatic = $derived(isAutomatic(pool));
  const editing = $derived(router.param('edit') === '1' && !automatic);
  /** Migrate reads installation_id, so it opens already scoped to this pool's App. */
  const migrateHref = $derived(
    pool?.installation_id ? `/migrate?installation_id=${pool.installation_id}` : '/migrate',
  );
  const runners = $derived(fleet.runnersInPool(id));
  const counts = $derived(pool?.counts ?? {});
  // Only where a runner's size is somebody's choice per host: a pool that takes
  // its size from each host, or a fleet where some host has been given a size or
  // a minimum. A fleet that has set none sees the page it always has.
  const showHostSizes = $derived(
    canOperate &&
      pool !== null &&
      pool !== undefined &&
      (pool.size_from_profile === true || fleet.hosts.some((h) => h.runner_profile !== undefined)),
  );

  $effect(() => {
    if (pool?.name) router.setTitle(pool.name);
  });

  /* -- what the controller says about the pools it keeps ---------------------- */

  // Read only for a pool the controller keeps, and only for what it says about
  // the classes and whether the pool may be deleted. A failed read leaves the
  // page without those two things, which it manages without.
  let autoStatus = $state<AutoPools | null>(null);
  $effect(() => {
    if (!automatic) {
      autoStatus = null;
      return;
    }
    const controller = new AbortController();
    void getAutoPools(controller.signal)
      .then((result) => (autoStatus = result))
      .catch(() => undefined);
    return () => controller.abort();
  });
  // Not while the controller is keeping the pool, because it would make it again,
  // which the server refuses too. One it is not keeping is a leftover.
  const deleteBlocked = $derived(automatic && pool?.auto?.kept === true);
  let autoOpen = $state(false);
  // `?edit=1` is how a link says "edit this pool", and a pool the controller keeps
  // answers it with its settings. The query is cleared so Back does not reopen it.
  $effect(() => {
    if (automatic && canOperate && router.param('edit') === '1') {
      autoOpen = true;
      router.setQuery({ edit: null }, { replace: true });
    }
  });

  /* -- recent jobs ----------------------------------------------------------- */

  let jobs = $state<Job[]>([]);
  let jobsLoading = $state(true);
  let jobsError = $state<unknown>(null);
  let jobsAttempt = $state(0);

  $effect(() => {
    const poolId = id;
    const retry = jobsAttempt;
    if (poolId === '' || retry < 0) return;
    const controller = new AbortController();
    jobsLoading = true;
    listJobs({ pool_id: [poolId], limit: JOB_LIMIT }, controller.signal)
      .then((response) => {
        jobs = response.items ?? [];
        jobsError = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        jobsError = cause;
      })
      .finally(() => {
        jobsLoading = false;
      });
    return () => controller.abort();
  });

  $effect(() => {
    const poolId = id;
    return events.subscribe('job.updated', (job) => {
      if (job.pool_id !== poolId) return;
      const known = jobs.some((existing) => existing.id === job.id);
      jobs = known
        ? jobs.map((existing) => (existing.id === job.id ? job : existing))
        : [job, ...jobs].slice(0, JOB_LIMIT);
    });
  });

  /* -- scaling history -------------------------------------------------------- */

  let scaling = $state<ScalingEvent[]>([]);
  let scalingLoading = $state(true);
  let scalingError = $state<unknown>(null);
  let scalingAttempt = $state(0);

  $effect(() => {
    const poolId = id;
    const retry = scalingAttempt;
    if (poolId === '' || retry < 0) return;
    const controller = new AbortController();
    scalingLoading = true;
    listScalingEvents({ pool_id: poolId, limit: SCALING_LIMIT }, controller.signal)
      .then((response) => {
        scaling = response.items ?? [];
        scalingError = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        scalingError = cause;
      })
      .finally(() => {
        scalingLoading = false;
      });
    return () => controller.abort();
  });

  $effect(() => {
    const poolId = id;
    return events.subscribe('scaling', (event) => {
      if (event.pool_id !== poolId) return;
      scaling = [event, ...scaling].slice(0, SCALING_LIMIT);
    });
  });

  /* -- actions ----------------------------------------------------------------- */

  /**
   * Everything this page reads, at once: the pool itself, the fleet cache the
   * runner list comes from, the recent jobs, and the scaling history. Three of
   * the four are fed by the stream in the ordinary case; asking for all four is
   * what makes one press answer for the whole page rather than a quarter of it.
   */
  function refreshPage(): Promise<void> {
    attempt += 1;
    jobsAttempt += 1;
    scalingAttempt += 1;
    return fleet.reconcile();
  }

  function setEnabled(enabled: boolean): void {
    if (!pool?.id) return;
    const poolId = pool.id;
    // For a pool the controller keeps, enabling and disabling are resuming and
    // pausing: the controller decides on its own whether the hosts leave it in
    // use, and the operator decides whether it may be.
    const patch =
      pool.auto !== undefined
        ? { auto: { ...pool.auto, paused: !enabled }, ...(enabled ? {} : { enabled: false }) }
        : { enabled };
    void fleet.optimistic(
      poolId,
      patch,
      () => (enabled ? enablePool(poolId) : disablePool(poolId)),
      automatic
        ? enabled
          ? 'That pool was not resumed'
          : 'That pool was not paused'
        : enabled
          ? 'That pool was not enabled'
          : 'That pool was not disabled',
    );
  }

  async function prewarm(): Promise<void> {
    if (!pool?.id) return;
    try {
      const result = await prewarmPool(pool.id);
      toasts.success('Image prewarm queued', `${pluralise(result.queued ?? 0, 'matching host')}.`);
    } catch (cause) {
      toasts.fromError(cause, 'The image was not prewarmed');
    }
  }

  let deleteOpen = $state(false);
  let limitsOpen = $state(false);
  let forceDelete = $state(false);

  // Every open starts on the drain: "Destroy its runners immediately"
  // interrupts work in progress, and a tick left by an earlier cancel must not
  // make it the default.
  function askDelete(): void {
    forceDelete = false;
    deleteOpen = true;
  }

  const consequences = $derived(
    deletionConsequences({ ...counts, queued: pool?.queued_jobs }, forceDelete),
  );

  async function confirmDelete(): Promise<boolean> {
    if (!pool?.id) return false;
    const name = pool.name ?? 'the pool';
    try {
      const result = await deletePool(pool.id, { drain: !forceDelete, force: forceDelete });
      const affected = result?.runners_affected ?? 0;
      toasts.success(
        `Deleted ${name}`,
        affected > 0
          ? `${pluralise(affected, 'runner')} ${forceDelete ? 'destroyed' : 'draining'}.`
          : undefined,
      );
      void fleet.reconcile();
      router.navigate('/pools');
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That pool was not deleted');
      return false;
    }
  }

  function startEditing(): void {
    if (automatic) autoOpen = true;
    else router.setQuery({ edit: '1' }, { replace: false });
  }

  function stopEditing(): void {
    router.setQuery({ edit: null }, { replace: false });
  }
</script>

<PageHeader
  title={pool?.name ?? 'Pool'}
  breadcrumb={[{ label: 'All pools', href: '/pools' }, { label: pool?.name ?? 'Pool' }]}
  subtitle={editing ? 'Change what this pool makes, and how many of them.' : undefined}
  onrefresh={refreshPage}
>
  {#snippet meta()}
    {#if pool}
      <Badge status={poolStatus(pool)} size="sm" />
      <Badge tone="neutral" size="sm" dot={false} label={backendLabel(pool.backend)} />
      {#if pool.auto}
        <Badge tone="accent" size="sm" dot={false} label="Automatic" title={pool.auto.summary} />
      {/if}
      {#if pool.installation_target}
        <span class="target">{pool.installation_target}</span>
      {/if}
      <span class="counts tabular">
        {formatNumber(counts.live ?? 0)} live · {formatNumber(counts.busy ?? 0)} busy ·
        {formatNumber(pool.queued_jobs ?? 0)} queued
      </span>
    {/if}
  {/snippet}

  {#if pool && canOperate && !editing}
    {#if !automatic}
      <Button icon={Gauge} onclick={() => (limitsOpen = true)}>Runner limits</Button>
    {/if}
    <Button icon={Download} onclick={prewarm}>Prewarm image</Button>
    {#if automatic}
      {#if pool.auto?.paused}
        <Button icon={Power} onclick={() => setEnabled(true)}>Resume</Button>
      {:else}
        <Button icon={PowerOff} onclick={() => setEnabled(false)}>Pause</Button>
      {/if}
    {:else if pool.enabled === false}
      <Button icon={Power} onclick={() => setEnabled(true)}>Enable</Button>
    {:else}
      <Button icon={PowerOff} onclick={() => setEnabled(false)}>Disable</Button>
    {/if}
    <Button variant="primary" icon={Pencil} onclick={startEditing}>
      {automatic ? 'Settings' : 'Edit'}
    </Button>
    <Button
      variant="danger"
      icon={Trash2}
      disabled={deleteBlocked}
      title={deleteBlocked
        ? 'The controller would make this pool again. Pause it to take it out of use, or set scheduler.auto_pools to shadow or off first.'
        : undefined}
      onclick={askDelete}>Delete</Button
    >
  {/if}
</PageHeader>

{#if error && !pool}
  <ErrorState {error} title="This pool could not be loaded" onretry={() => (attempt += 1)} />
{:else if !pool}
  <div class="loading" aria-busy={loading ? 'true' : undefined}>
    <Skeleton width="40%" height="1.25rem" />
    <Skeleton lines={4} />
  </div>
{:else if editing}
  {#key pool.id}
    <PoolWizardForm {pool} oncancel={stopEditing} ondone={stopEditing} />
  {/key}
{:else}
  <RunnerInsights poolId={pool.id ?? ''} compact />
  <div class="layout">
    <div class="main">
      <section class="panel" aria-labelledby="runners-heading">
        <div class="panel-head">
          <h2 id="runners-heading">Runners</h2>
          <span class="panel-note tabular">
            {formatNumber(pool.min_runners ?? 0)} minimum · {formatNumber(pool.max_runners ?? 0)} maximum
          </span>
        </div>
        <div class="utilisation">
          <UtilisationBar
            busy={counts.busy ?? 0}
            live={counts.live ?? 0}
            min={pool.min_runners}
            max={pool.max_runners}
            label="Runners in {pool.name ?? 'this pool'}"
          />
        </div>
        <PoolRunners {runners} loading={!fleet.loaded} allHref="/runners?pool_id={pool.id}" />
      </section>

      <section class="panel" aria-labelledby="jobs-heading">
        <div class="panel-head">
          <h2 id="jobs-heading">Recent jobs</h2>
          <a class="panel-link" href="/jobs?pool_id={pool.id}">All jobs on this pool</a>
        </div>
        <PoolJobs
          {jobs}
          loading={jobsLoading}
          error={jobsError}
          onretry={() => (jobsAttempt += 1)}
        />
      </section>

      <section class="panel" aria-labelledby="scaling-heading">
        <div class="panel-head">
          <h2 id="scaling-heading">Scaling history</h2>
          <span class="panel-note">The scheduler's own words</span>
        </div>
        <PoolScaling
          events={scaling}
          loading={scalingLoading}
          error={scalingError}
          onretry={() => (scalingAttempt += 1)}
        />
      </section>
    </div>

    <div class="side">
      {#if automatic}
        <section class="panel" aria-labelledby="auto-heading">
          <div class="panel-head">
            <h2 id="auto-heading">Automatic pool</h2>
          </div>
          <PoolAutoSummary {pool} status={autoStatus} />
        </section>
      {/if}

      <!--
        The last mile, and it used to be missing. RunsOnPreview appeared only on
        step two of the wizard and vanished the moment the pool existed -- so an
        operator who had just created their first pool was told runners would
        appear "as soon as a job asks for these labels" without being told what
        to write. This is the line they copy into a workflow.
      -->
      <section class="panel" aria-labelledby="runs-on-heading">
        <div class="panel-head">
          <h2 id="runs-on-heading">Point a workflow here</h2>
        </div>
        <div class="panel-body">
          <RunsOnPreview labels={pool.labels ?? []} />
          <TestJob labels={pool.labels ?? []} />
          <a class="migrate-link" href={migrateHref}>Rewrite runs-on across repositories</a>
        </div>
      </section>

      <section class="panel" aria-labelledby="warnings-heading">
        <div class="panel-head">
          <h2 id="warnings-heading">Warnings</h2>
        </div>
        <PoolWarnings warnings={pool.warnings ?? []} bare action={warningAction} />
      </section>

      <section class="panel" aria-labelledby="config-heading">
        <div class="panel-head">
          <h2 id="config-heading">Configuration</h2>
          {#if canOperate}
            <button type="button" class="panel-link" onclick={startEditing}>
              {automatic ? 'Settings' : 'Edit'}
            </button>
          {/if}
        </div>
        <PoolConfig {pool} />
      </section>

      {#if showHostSizes}
        <section class="panel" aria-labelledby="host-sizes-heading">
          <div class="panel-head">
            <h2 id="host-sizes-heading">Size on each host</h2>
          </div>
          <div class="panel-body">
            <PoolHostSizes {pool} />
          </div>
        </section>
      {/if}
    </div>
  </div>
{/if}

<PoolRunnerLimitsDialog bind:open={limitsOpen} pool={pool ?? null} />
<PoolAutoDialog bind:open={autoOpen} pool={pool ?? null} />

<!--
  A pool with nowhere to run carries the backends its hosts do offer, so the
  change the fix asks for is one click rather than a trip through the wizard.
-->
{#snippet warningAction(warning: Problem)}
  {#if pool && canOperate && warning.code === 'pool.no_capacity' && (warning.alternatives?.length ?? 0) > 0}
    <PoolBackendSwitch {pool} alternatives={(warning.alternatives ?? []) as BackendKind[]} />
  {/if}
{/snippet}

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete pool"
  name={pool?.name ?? ''}
  description="Deleting {pool?.name ??
    'this pool'} removes it from the controller. Queued jobs asking for its labels will stop matching anything."
  {consequences}
  confirmLabel="Delete pool"
  requireName
  onconfirm={confirmDelete}
  oncancel={() => (forceDelete = false)}
>
  <Checkbox
    bind:checked={forceDelete}
    label="Destroy its runners immediately"
    description="Without this, runners finish the job they are on and then exit. With it, work in progress is interrupted."
  />
</ConfirmDialog>

<style>
  .loading {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .target {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .counts {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1.7fr) minmax(0, 1fr);
    gap: var(--z-space-6);
    align-items: start;
  }
  .main,
  .side {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
    min-width: 0;
  }
  .panel {
    padding: var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .panel-body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-4) var(--z-space-5);
  }
  .migrate-link {
    font-size: var(--z-text-xs);
    color: var(--z-accent);
  }
  .panel-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--z-space-3);
    margin-bottom: var(--z-space-4);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .panel-note {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .panel-link {
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--z-accent);
    font: inherit;
    font-size: var(--z-text-xs);
    text-decoration: none;
    cursor: pointer;
  }
  .panel-link:hover {
    text-decoration: underline;
  }
  .utilisation {
    margin-bottom: var(--z-space-4);
  }
  @media (max-width: 1180px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
