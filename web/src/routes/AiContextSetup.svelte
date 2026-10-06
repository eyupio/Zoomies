<script lang="ts">
  import { listContextInstallations } from '$lib/api/client';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import MaintenanceWizard from '$lib/aicontext/MaintenanceWizard.svelte';
  import SetupWizard from '$lib/aicontext/SetupWizard.svelte';

  // Setup is open to an administrator and to anyone made owner of an
  // installation. The server decides per installation; this only chooses
  // between the wizard and an explanation.
  let owns = $state<boolean | null>(null);
  $effect(() => {
    const controller = new AbortController();
    void listContextInstallations(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) owns = (result.items ?? []).length > 0;
      })
      .catch(() => {
        if (!controller.signal.aborted) owns = false;
      });
    return () => controller.abort();
  });
  const allowed = $derived(session.can('admin') || owns === true);
</script>

<PageHeader
  title={router.param('mode') ? 'Manage AI Context' : 'Enable repositories'}
  subtitle={router.param('mode')
    ? 'Review repository changes before reinstalling, amending or removing AI Context.'
    : 'Choose repositories, output and readers. Review the setup before publishing context.'}
  breadcrumb={[{ label: 'AI Context', href: '/kennel/ai-context' }]}
/>
{#if owns === null && !session.can('admin')}<Skeleton lines={4} />
{:else if allowed}{#if router.param('mode')}<MaintenanceWizard
      id={router.param('draft_id')}
      mode={router.param('mode')}
    />{:else}<SetupWizard
      draftId={router.param('draft_id')}
      installationId={router.param('installation_id')}
    />{/if}{:else}<ErrorState
    title="Installation ownership required"
    description="Preparing repositories requires an administrator, or ownership of the GitHub installation, which an administrator can grant. Your existing source access is available from AI Context."
  />{/if}
