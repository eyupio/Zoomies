<!--
  The second half of the fix for a pool no host can run.

  "Point this pool at a backend they already offer" is a five-click detour
  through the editor when the controller already knows which backends the
  hosts do offer. This is that change as one button, with the consequences of
  each backend stated before it is made -- moving to the process backend takes
  jobs out of a container altogether, which is never something to discover
  afterwards.
-->
<script lang="ts">
  import { ArrowLeftRight } from '@lucide/svelte';
  import { updatePool } from '$lib/api/client';
  import type { BackendKind, Pool } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { backendSwitch } from './backend-switch';
  import { backendLabel } from './vocabulary';

  interface Props {
    pool: Pool;
    /** The backends the connected hosts already offer, from the problem. */
    alternatives: readonly BackendKind[];
  }

  let { pool, alternatives }: Props = $props();

  let chosen = $state<BackendKind | null>(null);
  let busy = $state(false);

  // The process backend cannot give a job a Docker daemon or a container to
  // measure, so a pool that had either loses it in the same change. Saying it
  // here is the difference between an informed switch and a surprise, and the
  // request carries exactly what is said.
  const change = $derived(chosen ? backendSwitch(pool, chosen) : null);

  const consequences = $derived.by(() => {
    if (!chosen) return [];
    const lines = [
      `New runners will be created with ${backendLabel(chosen)} instead of ${backendLabel(pool.backend)}.`,
      'Runners that already exist are left alone; each finishes its job and goes.',
    ];
    if (chosen === 'process') {
      lines.push(
        'Jobs will run as processes on the host rather than in a container, so a job can see and change the host filesystem.',
      );
    }
    lines.push(...(change?.switchedOff ?? []));
    return lines;
  });

  async function confirm(): Promise<boolean> {
    const backend = chosen;
    if (!backend || !pool.id) return false;
    busy = true;
    try {
      await updatePool(pool.id, backendSwitch(pool, backend).body);
      toasts.success(
        `${pool.name} now uses ${backendLabel(backend)}`,
        'The scheduler will place its next runner on a host that offers it.',
      );
      void fleet.reconcile();
      chosen = null;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That pool was not changed');
      return false;
    } finally {
      busy = false;
    }
  }
</script>

<div class="switch">
  {#each alternatives as backend (backend)}
    <Button size="sm" icon={ArrowLeftRight} onclick={() => (chosen = backend)}>
      Switch to {backendLabel(backend)}
    </Button>
  {/each}
</div>

<ConfirmDialog
  bind:open={
    () => chosen !== null,
    (open) => {
      // The dialog closes itself once the change is made or cancelled; keeping
      // the two in step is what lets the same button open it again.
      if (!open) chosen = null;
    }
  }
  tone="default"
  title="Change this pool's backend"
  name={pool.name}
  description="{pool.name} will ask for {backendLabel(
    chosen ?? pool.backend,
  )} runners, which your connected hosts already offer."
  {consequences}
  confirmLabel="Change backend"
  {busy}
  onconfirm={confirm}
/>

<style>
  .switch {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin-top: var(--z-space-3);
  }
</style>
