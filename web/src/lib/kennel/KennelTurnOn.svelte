<!--
  The button on a page that says Kennel Club is off. It does what the switch in
  the rail does, from the place that explains what it would be turning on.

  Only an administrator is shown it. Anybody else is told who can, by the page
  that holds it, since the sentence is about that page ("there is nothing to list
  until it is on") and not about the button.
-->
<script lang="ts">
  import { Eye } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import { KENNEL_NOW_ON } from '$lib/kennel/words';
  import { kennelClub } from '$lib/state/kennel.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';

  let busy = $state(false);
  let problem = $state('');

  async function turnOn(): Promise<void> {
    busy = true;
    problem = '';
    const result = await kennelClub.set(true);
    busy = false;
    if (result.ok) toasts.success(KENNEL_NOW_ON.title, KENNEL_NOW_ON.detail);
    else problem = result.message;
  }
</script>

{#if session.can('admin')}
  <Button variant="primary" icon={Eye} loading={busy} onclick={() => void turnOn()}
    >Turn on Kennel Club</Button
  >
  {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
{/if}

<style>
  .problem {
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-danger);
  }
</style>
