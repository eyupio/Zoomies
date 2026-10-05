<!--
  The suggested sidecar share, as one button.

  `pool.daemon_share_suggested` says which share to try, and a number to copy into
  a form is a worse way to take advice than a click. This makes the change the
  notice proposes and nothing else: the pool's own settings go back as they were,
  because an update replaces a pool's resources whole and sending only the shares
  would take its minimum with them.
-->
<script lang="ts">
  import { Check } from '@lucide/svelte';
  import { updatePool } from '$lib/api/client';
  import type { Problem } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import { withSuggestedShare } from './daemonShare';

  interface Props {
    problem: Problem;
  }

  let { problem }: Props = $props();

  let busy = $state(false);

  const change = $derived(problem.daemon_share);
  const pool = $derived(fleet.pool(problem.target_id));

  /** "20% of its CPU and 35% of its memory", for the button and the toast. */
  const words = $derived(
    [
      change?.cpu_percent ? `${change.cpu_percent}% of the CPU` : '',
      change?.memory_percent ? `${change.memory_percent}% of the memory` : '',
    ]
      .filter(Boolean)
      .join(' and '),
  );

  async function apply(): Promise<void> {
    if (!pool?.id || !change) return;
    const resources = withSuggestedShare(pool.resources, change);
    busy = true;
    try {
      await updatePool(pool.id, { resources });
      toasts.success(
        `${pool.name}: the sidecar now gets ${words}`,
        'Runners created from now on are divided this way; those already running finish as they are.',
      );
      void fleet.reconcile();
    } catch (cause) {
      toasts.fromError(cause, 'That pool was not changed');
    } finally {
      busy = false;
    }
  }
</script>

{#if change && pool && session.can('operator')}
  <Button size="sm" icon={Check} loading={busy} onclick={apply}>
    Give the sidecar {words}
  </Button>
{/if}
