<!--
  The change a problem proposes, as one button.

  A problem that says "give each runner here 2.25 CPU" and then leaves an
  operator to find the form is doing the cheap half of the job. The controller
  has already worked the change out and priced it against the fleet, so this
  sends the problem and not the change: the controller applies what it proposes
  now, as the person who clicked, through the pool's or the host's own update --
  so it is refused for the role that update refuses, and for a change that would
  leave a pool with nowhere to run -- and a proposal that has since changed is
  refused as out of date rather than made.
-->
<script lang="ts">
  import { Check } from '@lucide/svelte';
  import { applyRemedy } from '$lib/api/client';
  import type { Problem } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';

  interface Props {
    problem: Problem;
  }

  let { problem }: Props = $props();

  let busy = $state(false);

  const remedy = $derived(problem.remedy);

  async function apply(): Promise<void> {
    if (!remedy || !problem.target_id) return;
    busy = true;
    try {
      await applyRemedy({ code: problem.code, target_id: problem.target_id, remedy_id: remedy.id });
      toasts.success(remedy.label, remedy.effect || 'Applies to runners created from now on.');
      void fleet.reconcile();
    } catch (cause) {
      toasts.fromError(cause, 'That change was not made');
    } finally {
      busy = false;
    }
  }
</script>

{#if remedy && problem.target_id && session.can('operator')}
  <span class="remedy">
    <Button size="sm" icon={Check} loading={busy} onclick={apply}>{remedy.label}</Button>
    {#if remedy.effect}<span class="effect">{remedy.effect}</span>{/if}
  </span>
{/if}

<style>
  .remedy {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2) var(--z-space-3);
  }
  .effect {
    font-size: var(--z-text-sm);
    color: var(--z-text-subtle);
  }
</style>
