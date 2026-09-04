<script lang="ts">
  import { Search } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
</script>

<PageHeader title="Page not found" subtitle="Nothing in Zoomies answers to this address." />

<EmptyState
  icon={Search}
  title="That address does not exist"
  description="It may have been a link to something that has since been deleted. The overview is a good place to start again."
>
  <Button variant="primary" href="/">Go to the overview</Button>
</EmptyState>
