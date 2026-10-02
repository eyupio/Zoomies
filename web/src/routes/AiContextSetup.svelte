<script lang="ts">
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SetupWizard from '$lib/aicontext/SetupWizard.svelte';
</script>

<PageHeader
  title="Enable repositories"
  subtitle="Choose repositories, output and readers. Review the setup before publishing context."
  breadcrumb={[{ label: 'AI Context', href: '/ai-context' }]}
/>
{#if session.can('admin')}<SetupWizard
    draftId={router.param('draft_id')}
    installationId={router.param('installation_id')}
  />{:else}<ErrorState
    title="Administrator access required"
    description="Preparing repositories and assigning source readers requires an administrator. Your existing source access is available from AI Context."
  />{/if}
