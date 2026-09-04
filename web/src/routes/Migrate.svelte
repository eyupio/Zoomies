<!--
  Moving repositories onto this fleet.

  The page is a shell: the wizard owns the flow, so that the same five steps can
  be reached from an installation's page later without a second copy of them.
-->
<script lang="ts">
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import MigrateWizard from '$lib/migrate/MigrateWizard.svelte';

  const canOperate = $derived(session.can('operator'));
  // A link can point straight at one installation, which is how the
  // Installations page hands off into this.
  const installationId = $derived(router.param('installation_id'));

  function cancel(): void {
    router.navigate('/installations');
  }
</script>

<PageHeader
  title="Migrate repositories"
  subtitle="Rewrite runs-on in your workflows and open a pull request on each repository. You see the exact diff before anything is opened."
/>

{#if canOperate}
  <MigrateWizard {installationId} oncancel={cancel} />
{:else}
  <ErrorState
    title="Not allowed"
    description="Opening pull requests in your organisation's repositories needs the operator role. An administrator can grant it under Settings."
  />
{/if}
