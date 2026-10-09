<!--
  Where to find Eli. The conversation itself is the widget at the corner of every
  page, so there is one place to talk to Eli and it is the same one everywhere;
  this card says so, opens it, and says whether Eli can see the fleet through the
  provider that answers.
-->
<script lang="ts">
  import { PawPrint } from '@lucide/svelte';
  import type { AssistantProvider } from '$lib/api/types';
  import EliAvatar from '$lib/assistant/EliAvatar.svelte';
  import { eli } from '$lib/assistant/eli.svelte';
  import Button from '$lib/components/Button.svelte';

  interface Props {
    providers: readonly AssistantProvider[];
  }
  let { providers }: Props = $props();

  const answering = $derived(providers.find((p) => p.is_default && p.enabled));
</script>

<section class="eli" aria-labelledby="assistant-eli">
  <EliAvatar size={40} />
  <div class="text">
    <h3 id="assistant-eli">Eli</h3>
    {#if answering}
      <p>
        Eli answers through <strong>{answering.name}</strong>, {answering.model}, and is in the
        corner of every page, or press <kbd>E</kbd>.
        {#if answering.fleet_access}
          Eli can read this fleet through it, and cannot change anything.
        {:else}
          Eli cannot see this fleet through it: switch on <em>Let Eli read this fleet</em> in the provider's
          settings to allow that.
        {/if}
      </p>
    {:else}
      <p>
        Add a provider, test it and make it the default, and Eli appears in the corner of every
        page.
      </p>
    {/if}
  </div>
  {#if answering}
    <Button icon={PawPrint} onclick={() => eli.show()}>Open Eli</Button>
  {/if}
</section>

<style>
  .eli {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3) var(--z-space-4);
    margin-bottom: var(--z-space-6);
    padding: var(--z-space-4);
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-lg);
  }
  .text {
    flex: 1;
    min-width: 16rem;
  }
  h3,
  p {
    margin: 0;
  }
  h3 {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  p {
    margin-top: var(--z-space-1);
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  kbd {
    padding: var(--z-nudge-1) var(--z-space-1);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
  }
</style>
