<!--
  Ask Eli something, with the model the cards above chose.

  This is the first way to use a provider once it is set up: a conversation the
  page holds in memory and sends whole with each question. The controller keeps
  nothing, so closing the page ends it. Eli cannot see the fleet yet and says so.

  An answer is drawn from Markdown by our own renderer, which makes elements and
  never injects HTML: what a model writes is not markup the page should trust.
-->
<script lang="ts">
  import { Trash2 } from '@lucide/svelte';
  import type { AssistantProvider } from '$lib/api/types';
  import { Conversation } from '$lib/assistant/conversation.svelte';
  import ConversationView from '$lib/assistant/ConversationView.svelte';
  import Button from '$lib/components/Button.svelte';

  interface Props {
    providers: readonly AssistantProvider[];
  }
  let { providers }: Props = $props();

  const conversation = new Conversation();
  const answering = $derived(providers.find((p) => p.is_default && p.enabled));
</script>

<section class="chat" aria-labelledby="assistant-chat">
  <header>
    <div>
      <h3 id="assistant-chat">Ask Eli</h3>
      {#if answering}
        <p class="who">
          Answers from <strong>{answering.name}</strong>, {answering.model}. Eli cannot see this
          fleet yet, and cannot change anything.
        </p>
      {:else}
        <p class="who">Add a provider, test it and make it the default to ask Eli something.</p>
      {/if}
    </div>
    {#if conversation.turns.length > 0}
      <Button variant="ghost" icon={Trash2} onclick={() => conversation.clear()}
        >New conversation</Button
      >
    {/if}
  </header>

  <ConversationView {conversation} {answering} />
</section>

<style>
  .chat {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin-bottom: var(--z-space-6);
    padding-bottom: var(--z-space-6);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  h3,
  p {
    margin: 0;
  }
  h3 {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .who {
    margin-top: var(--z-space-1);
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
</style>
