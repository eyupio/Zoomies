<!--
  Adding a host.

  A page rather than a dialog, because the interesting part happens on another
  machine: the operator pastes a command there and comes back to see whether
  it worked. A dialog closes when a hand slips, and with it goes the only copy
  of the token; a page survives the tab switch, keeps watching while the
  command runs, and says when the host has arrived.
-->
<script lang="ts">
  import { session } from '$lib/state/session.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import AddHostFlow from '$lib/hosts/AddHostFlow.svelte';

  const canAdmin = $derived(session.can('admin'));
</script>

<PageHeader
  title="Add a host"
  breadcrumb={[{ label: 'Hosts', href: '/hosts' }, { label: 'Add a host' }]}
  subtitle="Describe the machine, run one command on it, and watch it join."
/>

{#if canAdmin}
  <AddHostFlow />
{:else}
  <ErrorState
    title="Not allowed"
    description="Enrolling a host needs the admin role, because a join token turns any machine into part of this fleet. An administrator can add it, or grant the role under Settings."
  />
{/if}
