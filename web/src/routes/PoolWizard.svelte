<!--
  Creating a pool.

  The wizard itself lives in $lib/pools so that editing an existing pool, on the
  pool's own page, is the same five steps rather than a second form that drifts
  away from this one.
-->
<script lang="ts">
  import type { Pool } from '$lib/api/types';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import PoolWizardForm from '$lib/pools/PoolWizardForm.svelte';

  const canOperate = $derived(session.can('operator'));

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
  subtitle="Five steps: who the runners register with, what labels they answer to, how they run, how many there are, and what the controller makes of it."
/>

{#if canOperate}
  <PoolWizardForm oncancel={cancel} ondone={done} />
{:else}
  <ErrorState
    title="Not allowed"
    description="Creating a pool needs the operator role. An administrator can grant it under Settings, or create the pool for you."
  />
{/if}
