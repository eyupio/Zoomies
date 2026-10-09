<!--
  One model the assistant may talk to.

  The key is never on the card: "set, never shown" is the whole of what the
  page says about it, because a field that renders dots has been sent the
  secret to render them. The check line is the controller's own result, kept
  on the row, so a reload shows what the last Test learned.
-->
<script lang="ts">
  import { Pencil, Star, Stethoscope, Trash2 } from '@lucide/svelte';
  import type { AssistantProvider } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import { checkSummary, KIND_LABELS } from './assistant';

  interface Props {
    provider: AssistantProvider;
    checking?: boolean;
    busy?: boolean;
    oncheck: (provider: AssistantProvider) => void;
    ondefault: (provider: AssistantProvider) => void;
    ontoggle: (provider: AssistantProvider, enabled: boolean) => void;
    onedit: (provider: AssistantProvider) => void;
    onremove: (provider: AssistantProvider) => void;
  }

  let {
    provider,
    checking = false,
    busy = false,
    oncheck,
    ondefault,
    ontoggle,
    onedit,
    onremove,
  }: Props = $props();

  const failed = $derived(provider.last_check != null && !provider.last_check.ok);
  const needsKey = $derived(
    provider.kind !== 'fake' && provider.kind !== 'openai_compatible' && !provider.key_configured,
  );
</script>

<article class="card" aria-labelledby="assistant-{provider.id}-name">
  <header>
    <h3 id="assistant-{provider.id}-name">{provider.name}</h3>
    <div class="badges">
      <!-- Neutral and accent only: which protocol this speaks and whether it
           is the default are facts about a provider, not states of a runner. -->
      <Badge
        tone="neutral"
        label={KIND_LABELS[provider.kind] ?? provider.kind}
        size="sm"
        dot={false}
      />
      {#if provider.is_default}
        <Badge
          tone="accent"
          label="Default"
          size="sm"
          dot={false}
          title="The provider that answers."
        />
      {/if}
      {#if provider.local}
        <Badge
          tone="accent"
          label="Local"
          size="sm"
          dot={false}
          title="Its address is this machine or a private network."
        />
      {/if}
      {#if !provider.enabled}
        <Badge tone="draining" label="Disabled" size="sm" dot={false} />
      {/if}
      {#if needsKey}
        <Badge
          tone="pending"
          label="No key"
          size="sm"
          dot={false}
          title="This kind needs an API key before it can answer."
        />
      {/if}
    </div>
  </header>

  <p class="meta">
    <span>{provider.model}</span>
    {#if provider.base_url}<span class="mono">{provider.base_url}</span>{/if}
    {#if provider.key_configured}<span>Key set, never shown</span>{/if}
  </p>

  <p class="check" class:failed>
    {checkSummary(provider.last_check)}
    {#if provider.last_check?.checked_at}
      <span class="when">· <RelativeTime value={provider.last_check.checked_at} plain /></span>
    {/if}
  </p>

  <div class="actions">
    <Button
      size="sm"
      icon={Stethoscope}
      disabled={checking || busy}
      onclick={() => oncheck(provider)}
    >
      {checking ? 'Testing…' : 'Test'}
    </Button>
    <Button
      size="sm"
      icon={Star}
      variant="secondary"
      disabled={provider.is_default || busy}
      onclick={() => ondefault(provider)}
    >
      {provider.is_default ? 'Is the default' : 'Set as default'}
    </Button>
    <Button
      size="sm"
      variant="secondary"
      disabled={busy}
      onclick={() => ontoggle(provider, !provider.enabled)}
    >
      {provider.enabled ? 'Disable' : 'Enable'}
    </Button>
    <Button size="sm" icon={Pencil} variant="ghost" disabled={busy} onclick={() => onedit(provider)}
      >Edit</Button
    >
    <Button
      size="sm"
      icon={Trash2}
      variant="danger"
      disabled={busy}
      onclick={() => onremove(provider)}>Remove</Button
    >
  </div>
</article>

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    min-width: 0;
  }
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2) var(--z-space-3);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2) var(--z-space-4);
    margin: 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .mono {
    font-family: var(--z-font-mono);
    overflow-wrap: anywhere;
  }
  .check {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-accent);
  }
  .check.failed {
    color: var(--z-danger);
  }
  .when {
    color: var(--z-text-muted);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
</style>
