<!--
  A wrong address. The mark is here on purpose: a 404 is the one page most
  likely to be reached from somebody else's stale link, so it says which
  product refused the address before it says anything else.
-->
<script lang="ts">
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
</script>

<PageHeader title="Page not found" subtitle="Nothing in Zoomies answers to this address." />

<EmptyState
  title="That address does not exist"
  description="It may have been a link to something that has since been deleted. The overview is a good place to start again."
>
  {#snippet visual()}
    <Logo variant="mark" size={48} label="" />
  {/snippet}
  <Button variant="primary" href="/">Go to the overview</Button>
</EmptyState>
