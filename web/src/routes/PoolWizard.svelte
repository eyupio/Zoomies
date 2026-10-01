<!--
  Creating a pool.

  The wizard itself lives in $lib/pools so that editing an existing pool, on the
  pool's own page, is the same form rather than a second one that drifts away
  from this one.

  A pool belongs to a GitHub App installation, so with none there is nothing to
  fill in. The wizard used to find that out on its second step, after a click
  through the first and a read of its explainer; this says it before anything
  is asked, with the button that fixes it.
-->
<script lang="ts">
  import { Plug } from '@lucide/svelte';
  import { listInstallations } from '$lib/api/client';
  import type { Pool } from '$lib/api/types';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import PoolWizardForm from '$lib/pools/PoolWizardForm.svelte';

  const canOperate = $derived(session.can('operator'));
  const canAdmin = $derived(session.can('admin'));

  /**
   * How many installations there are: undefined while that is being asked, and
   * null when it could not be. The form is shown for null, because it carries
   * its own explanation of a failed listing and a guard that cannot tell is
   * not a reason to withhold the form.
   */
  let installations = $state<number | null | undefined>(undefined);
  $effect(() => {
    if (!canOperate) return;
    const request = new AbortController();
    listInstallations(request.signal).then(
      (result) => (installations = (result.items ?? []).length),
      () => {
        if (!request.signal.aborted) installations = null;
      },
    );
    return () => request.abort();
  });

  function cancel(): void {
    router.navigate('/pools');
  }

  function done(pool: Pool): void {
    router.navigate(pool.id ? `/pools/${pool.id}` : '/pools');
  }
</script>

<PageHeader
  title="Create a pool"
  breadcrumb={[{ label: 'Pools', href: '/pools' }, { label: 'Create a pool' }]}
  subtitle="Name it, label it, create it. Everything else has a default you can change later."
/>

{#if canOperate}
  {#if installations === undefined}
    <Skeleton height="12rem" />
  {:else if installations === 0}
    <EmptyState
      icon={Plug}
      title="Connect GitHub first"
      description="A pool registers its runners with a GitHub App installation, so Zoomies needs one before it can make a pool."
    >
      {#if canAdmin}
        <Button variant="primary" href="/installations?connect=1">Connect GitHub</Button>
      {:else}
        <p class="need-admin">An administrator can connect one.</p>
      {/if}
    </EmptyState>
  {:else}
    <PoolWizardForm oncancel={cancel} ondone={done} />
  {/if}
{:else}
  <ErrorState
    title="Not allowed"
    description="Creating a pool needs the operator role. An administrator can grant it under Settings, or create the pool for you."
  />
{/if}

<style>
  .need-admin {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
</style>
