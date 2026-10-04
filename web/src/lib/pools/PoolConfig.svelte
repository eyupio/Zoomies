<!--
  A pool's configuration, read-only.

  Deliberately a definition list and not a disabled form: an operator reading a
  pool at 2am is answering "what is this thing set to", and a grid of greyed-out
  inputs answers that far worse than plain text does. Changing it is the wizard's
  job, one deliberate click away.
-->
<script lang="ts">
  import type { Pool, RunnerSettings } from '$lib/api/types';
  import { formatBytes, formatGoDuration, formatMegabytes, formatNumber } from '$lib/format';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import PoolLabels from './PoolLabels.svelte';
  import { backendLabel, dockerModeLabel, platformLabelOrAny } from './PoolVocabulary.svelte';

  interface Props {
    pool: Pool;
    /** Off while the pool is still only a draft and has no ID yet. */
    showId?: boolean;
    class?: string;
  }

  let { pool, showId = true, class: className = '' }: Props = $props();

  const resources = $derived(pool.resources ?? {});
  const dind = $derived((pool.docker_mode ?? 'none') === 'dind');
  // Only CPU and memory decide the question. Disk and the pids limit have no
  // share to be given, so a pool may cap its cache's disk and still leave its
  // size to the host.
  const hasSize = $derived((resources.cpus ?? 0) > 0 || (resources.memory_mb ?? 0) > 0);
  // The server says which of the two this is rather than the browser inferring
  // it from two absent numbers -- "no CPU limit" alone cannot tell "the host
  // decides" from "nobody set one", and those used to be the same thing.
  const automatic = $derived((pool.sizing ?? (hasSize ? 'fixed' : 'automatic')) !== 'fixed');
  // A pool can leave its size to the host in two ways: by the share the host's
  // slot count gives, or by the standard size the host names for itself. The
  // second says where the answer is, and what stands in where a host says none.
  const fromProfile = $derived(pool.sizing === 'profile' || pool.size_from_profile === true);
  const fleetStandard = $derived(pool.fleet_standard);
  /* The minimum in force, not the one stored: a pool that set none follows the
     fleet's runners.minimum_* live, and saying so is the only way an operator
     learns that editing the fleet setting will move this pool. */
  const minimum = $derived(pool.effective_minimum);
  const hasMinimum = $derived((minimum?.cpus ?? 0) > 0 || (minimum?.memory_mb ?? 0) > 0);

  /* Only the timings this pool actually overrides. The rest follow the fleet,
     and a row per setting saying "the fleet's" would bury the two that do not. */
  const OVERRIDE_LABELS: readonly { key: keyof RunnerSettings; label: string }[] = [
    { key: 'provision_timeout', label: 'Provision timeout' },
    { key: 'drain_timeout', label: 'Drain timeout' },
    { key: 'max_runner_lifetime', label: 'Maximum lifetime' },
    { key: 'scale_up_delay', label: 'Scale-up delay' },
    { key: 'docker_wait', label: 'Docker wait' },
  ];
  const overrides = $derived.by(() => {
    const settings = pool.runner_settings ?? {};
    const out: { label: string; value: string }[] = [];
    for (const { key, label } of OVERRIDE_LABELS) {
      const value = settings[key];
      if (value === undefined || value === null) continue;
      out.push({ label, value: formatGoDuration(value) || String(value) });
    }
    return out;
  });

  const selector = $derived(Object.entries(pool.host_selector ?? {}));
  const env = $derived(Object.entries(pool.env ?? {}));
</script>

<dl class="config {className}">
  <div class="pair">
    <dt>Labels</dt>
    <dd><PoolLabels labels={pool.labels ?? []} max={12} wrap /></dd>
  </div>
  {#if automatic}
    <div class="pair">
      <dt>Elastic CPU</dt>
      <dd class="tabular">
        {pool.cpu_burst?.mode === 'automatic'
          ? `Automatic${(pool.cpu_burst.max_cpus ?? 0) > 0 ? `, up to ${formatNumber(pool.cpu_burst.max_cpus)} CPU` : ', up to the host ceiling'}${pool.cpu_burst.size_for_ceiling === false ? '; builds sized for the guarantee' : '; builds sized for the ceiling'}`
          : pool.cpu_burst?.mode === 'observe'
            ? 'Observe only'
            : 'Off'}
      </dd>
    </div>
  {/if}

  <div class="pair">
    <dt>GitHub target</dt>
    <dd>{pool.installation_target ?? 'Not set'}</dd>
  </div>

  <div class="pair">
    <dt>Runner group</dt>
    <dd>{pool.runner_group ? pool.runner_group : 'Default'}</dd>
  </div>

  <div class="pair">
    <dt>Backend</dt>
    <dd>{backendLabel(pool.backend)}</dd>
  </div>

  <div class="pair">
    <dt>Platform</dt>
    <dd>{platformLabelOrAny(pool.platform)}</dd>
  </div>

  <div class="pair">
    <dt>Docker in jobs</dt>
    <dd class:flagged={(pool.docker_mode ?? 'none') !== 'none'}>
      {dockerModeLabel(pool.docker_mode)}
    </dd>
  </div>

  <div class="pair">
    <dt>Runner lifetime</dt>
    <dd class:flagged={pool.ephemeral === false}>
      {pool.ephemeral === false
        ? 'Reused between jobs'
        : 'One job per runner, then it is destroyed'}
    </dd>
  </div>

  {#if pool.no_default_labels === true}
    <div class="pair">
      <dt>Default labels</dt>
      <dd>Left out — runners advertise only this pool’s labels</dd>
    </div>
  {/if}

  <div class="pair">
    <dt>Runs as root</dt>
    <dd class:flagged={pool.run_as_root === true}>{pool.run_as_root === true ? 'Yes' : 'No'}</dd>
  </div>

  <div class="pair">
    <dt>Scale</dt>
    <dd class="tabular">
      {formatNumber(pool.min_runners ?? 0)} minimum, {formatNumber(pool.max_runners ?? 0)} maximum
    </dd>

    <dt>Priority</dt>
    <dd>{formatNumber(pool.priority ?? 0)}</dd>
  </div>

  <div class="pair">
    <dt>Idle timeout</dt>
    <dd>{formatGoDuration(pool.idle_timeout) || 'Not set'}</dd>
  </div>

  {#if pool.image || pool.effective_image}
    <div class="pair">
      <dt>Image</dt>
      <dd>
        <code>{pool.image || pool.effective_image}</code>
        {#if !pool.image}
          <span class="note">Chosen by the platform above.</span>
        {/if}
      </dd>
    </div>
  {/if}

  <div class="pair">
    <dt>Pull policy</dt>
    <dd><code>{pool.pull_policy ?? 'if-not-present'}</code></dd>
  </div>

  {#if pool.runner_version}
    <div class="pair">
      <dt>Runner version</dt>
      <dd><code>{pool.runner_version}</code></dd>
    </div>
  {/if}

  <div class="pair">
    <dt>Size per runner</dt>
    <dd class="tabular">
      {#if fromProfile}
        <span>The size each host sets</span>
      {:else if automatic}
        <span>One share of each host</span>
      {:else}
        {#if resources.cpus !== undefined}<span>{formatNumber(resources.cpus)} CPU</span>{/if}
        {#if resources.memory_mb !== undefined}<span>{formatMegabytes(resources.memory_mb)}</span
          >{/if}
      {/if}
      {#if resources.disk_gb !== undefined}<span>{formatNumber(resources.disk_gb)} GB disk</span
        >{/if}
      {#if resources.pids_limit !== undefined}<span
          >{formatNumber(resources.pids_limit)} processes</span
        >{/if}
    </dd>
  </div>
  {#if minimum && hasMinimum}
    <div class="pair">
      <dt>Minimum per runner</dt>
      <dd class="tabular">
        {#if minimum.cpus > 0}<span
            >{formatNumber(minimum.cpus)} CPU{#if minimum.cpus_inherited}
              <span class="inherited">(fleet default)</span>{/if}</span
          >{/if}
        {#if minimum.memory_mb > 0}<span
            >{formatMegabytes(minimum.memory_mb)}{#if minimum.memory_mb_inherited}
              <span class="inherited">(fleet default)</span>{/if}</span
          >{/if}
      </dd>
    </div>
  {/if}
  <!-- What a pool that sets nothing is charged, which is not nothing: it is
       charged one slot's worth of whatever host the runner lands on, and given
       exactly that as a real limit. Saying so here is what stops "no limits"
       reading as "no reservation", which is the difference between a fleet
       that admits what it always did and one an operator thinks is unbounded. -->
  <div class="pair">
    <dt class="sr-only">Note</dt>
    <dd class="note">
      {#if fromProfile}
        Each runner is given the standard size of the host it lands on, as that host's runner sizes
        say.{#if fleetStandard && ((fleetStandard.cpus ?? 0) > 0 || (fleetStandard.memory_mb ?? 0) > 0)}
          A host that sets none gives the fleet's default, {formatNumber(fleetStandard.cpus ?? 0)} CPU
          and {formatMegabytes(fleetStandard.memory_mb ?? 0)}.{/if}{#if dind}
          Its runner and its Docker daemon share that size, so a slot here is one runner like
          anywhere else.{/if}
      {:else if automatic}
        Each runner is given one slot's share of the machine it lands on — the same share the fleet
        charges its host — so this pool is sized correctly on every host, and follows one that is
        resized.{#if dind}
          Its runner and its Docker daemon share that slot, so a slot here is one runner like
          anywhere else.{/if}
      {:else}
        A runner is charged this against its host, wherever it lands.{#if dind}
          Twice over: the build runs in a Docker daemon beside it, which is given the same limits.{/if}
      {/if}
    </dd>
  </div>

  {#if overrides.length > 0}
    <div class="pair">
      <dt>Runner timings</dt>
      <dd>
        <ul class="overrides">
          {#each overrides as override (override.label)}
            <li>
              <span class="override-name">{override.label}</span>
              <span class="tabular">{override.value}</span>
            </li>
          {/each}
        </ul>
      </dd>
    </div>
    <div class="pair">
      <dt class="sr-only">Note</dt>
      <dd class="note">
        Everything else about a runner's life follows this fleet's own settings, and keeps following
        them when they change.
      </dd>
    </div>
  {/if}

  {#if pool.cache?.enabled}
    <div class="pair">
      <dt>Cache</dt>
      <dd>
        {pool.cache.scope === 'repository' ? 'Per repository' : 'Shared across the pool'}{pool.cache
          .repository
          ? ` (${pool.cache.repository})`
          : ''},
        {pool.cache.size_limit ? `up to ${formatBytes(pool.cache.size_limit)}` : 'no size limit'}
        {#if pool.cache.source}
          <code>{pool.cache.source}</code>
        {/if}
      </dd>
    </div>
  {/if}

  {#if selector.length > 0}
    <div class="pair">
      <dt>Host selector</dt>
      <dd>
        {#each selector as [key, value] (key)}
          <code>{key}={value}</code>
        {/each}
      </dd>
    </div>
  {/if}

  {#if env.length > 0}
    <div class="pair">
      <dt>Environment</dt>
      <dd>
        {#each env as [key] (key)}
          <code>{key}</code>
        {/each}
        <span class="note">Values are not shown here.</span>
      </dd>
    </div>
  {/if}

  {#if showId}
    <div class="pair">
      <dt>Pool ID</dt>
      <dd><CopyButton value={pool.id ?? ''} label="Copy the pool ID" showValue /></dd>
    </div>
  {/if}
</dl>

<style>
  .config {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
  }
  .pair {
    display: grid;
    grid-template-columns: 11rem minmax(0, 1fr);
    gap: var(--z-space-4);
    align-items: baseline;
  }
  dt {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  dd {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    min-width: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  dd.flagged {
    color: var(--z-pending);
    font-weight: var(--z-weight-medium);
  }
  code {
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
  }
  .overrides {
    display: grid;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .overrides li {
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-3);
  }

  .override-name {
    color: var(--z-text-muted);
  }

  .inherited {
    color: var(--z-text-muted);
  }

  /* A note is prose, not a row of values, so it is not the flex row every other
     description is, and it spans the term's column too: it has no visible term.
     It lives in the list, with a hidden term of its own, because a paragraph is
     not allowed to sit directly inside a definition list. */
  dd.note {
    display: block;
    grid-column: 1 / -1;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  @media (max-width: 768px) {
    .pair {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--z-space-1);
    }
  }
</style>
