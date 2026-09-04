<!--
  Where GitHub sends the operator back to.

  The App manifest carries this address as both `redirect_url` and `setup_url`,
  so GitHub returns here twice with different luggage: once after creating the
  App, with `code` and `state`, and again after the App is installed, with
  `installation_id`. It is a landing strip, not a page -- everything it is
  handed is passed straight to the Installations page, which owns the flow and
  can pick it up at the right step.

  It exists as a route of its own because the address is baked into every App
  created from this controller: an App made a year ago still points here, and a
  path the router does not know is a "Page not found" in the middle of a
  connection flow.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { href, router } from '$lib/router';
  import Skeleton from '$lib/components/Skeleton.svelte';

  onMount(() => {
    const query = new URLSearchParams(location.search);
    // Replace rather than push: the code is single use, so going back to this
    // address would try to spend it a second time.
    router.navigate(
      href('/installations', {
        code: query.get('code'),
        state: query.get('state'),
        installation_id: query.get('installation_id'),
        setup_action: query.get('setup_action'),
      }),
      { replace: true },
    );
  });
</script>

<div aria-busy="true">
  <Skeleton width="220px" height="1.75rem" />
  <Skeleton width="320px" height="1rem" />
  <span class="sr-only">Returning you to the installations page</span>
</div>

<style>
  div {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
</style>
