<script module lang="ts">
  import type { Pool } from '$lib/api/types';

  /**
   * The dangerous settings a pool can carry, in our own words.
   *
   * The server's `warnings` are authoritative and are preferred whenever it
   * sent any; these exist so a pool that is plainly dangerous still says why
   * when the controller is an older build that did not fill the field in.
   */
  const LOCAL_RISKS: Readonly<Record<string, string>> = {
    'docker.host-socket':
      'The host Docker socket is handed to jobs, so any job on this pool can become root on the host.',
    'docker.dind': 'Jobs get a privileged Docker daemon of their own.',
    reused:
      'Runners are reused between jobs, so one job can leave files, credentials or processes behind for the next.',
    root: 'Jobs run as root inside the runner.',
  };

  /** True when this pool has a setting worth warning about. */
  export function poolHasRisk(pool: Pool): boolean {
    if ((pool.warnings?.length ?? 0) > 0) return true;
    if ((pool.docker_mode ?? 'none') !== 'none') return true;
    if (pool.ephemeral === false) return true;
    return pool.run_as_root === true;
  }

  /** Every risk this pool carries, one sentence each, most severe first. */
  export function poolRisks(pool: Pool): string[] {
    const fromServer = (pool.warnings ?? [])
      .map((w) => (w.detail ? `${w.title} ${w.detail}` : w.title))
      .filter((line): line is string => Boolean(line));
    if (fromServer.length > 0) return fromServer;

    const risks: string[] = [];
    const mode = pool.docker_mode ?? 'none';
    if (mode === 'host-socket') risks.push(LOCAL_RISKS['docker.host-socket'] ?? '');
    else if (mode === 'dind') risks.push(LOCAL_RISKS['docker.dind'] ?? '');
    if (pool.run_as_root === true) risks.push(LOCAL_RISKS['root'] ?? '');
    if (pool.ephemeral === false) risks.push(LOCAL_RISKS['reused'] ?? '');
    return risks.filter(Boolean);
  }
</script>

<!--
  The grid's warning marker.

  Colour is not doing the work here: the badge carries a triangle and a count,
  and the specific risk is in the tooltip *and* in text for assistive
  technology, never in the tooltip alone.
-->
<script lang="ts">
  import { pluralise } from '$lib/format';
  import { severityStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';

  interface Props {
    pool: Pool;
    class?: string;
  }

  let { pool, class: className = '' }: Props = $props();

  const risks = $derived(poolRisks(pool));
  const status = $derived(severityStatus('warning'));
  const summary = $derived(risks.join(' '));
</script>

{#if risks.length > 0}
  <Tooltip text={summary} placement="left" class={className}>
    <Badge {status} size="sm" label={pluralise(risks.length, 'risk')} />
  </Tooltip>
{:else}
  <span class="none {className}" aria-hidden="true">--</span>
{/if}

<style>
  .none {
    color: var(--z-text-subtle);
  }
</style>
