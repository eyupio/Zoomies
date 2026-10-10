<!--
  Assistant: which model answers, and where its traffic may go.

  Providers are cards, because an instance can have a local model and a hosted
  one and choose between them. The two switches under the cards are the
  settings rows assistant.allow_private_provider and assistant.local_only,
  read and written through the settings API like every other row, so the
  Configuration page and this one never disagree.
-->
<script lang="ts">
  import { MessageSquare, Plus } from '@lucide/svelte';
  import {
    ApiError,
    checkAssistantProvider,
    deleteAssistantProvider,
    getSettings,
    listAssistantProviders,
    setDefaultAssistantProvider,
    updateAssistantProvider,
    updateSettings,
  } from '$lib/api/client';
  import type { AssistantProvider, Setting } from '$lib/api/types';
  import type { AssistantScope } from '$lib/api/client';
  import { session } from '$lib/state/session.svelte';
  import EliRepairs from './EliRepairs.svelte';
  import { supportHint } from '$lib/errors';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import AssistantChat from './AssistantChat.svelte';
  import AssistantProviderCard from './AssistantProviderCard.svelte';
  import AssistantProviderForm from './AssistantProviderForm.svelte';

  const PRIVATE_KEY = 'assistant.allow_private_provider';
  const LOCAL_ONLY_KEY = 'assistant.local_only';

  let scope = $state<AssistantScope>('personal');
  let providerRevision = $state(0);
  let loadRevision = 0;
  let providers = $state<readonly AssistantProvider[]>([]);
  let settings = $state<readonly Setting[]>([]);
  let loading = $state(true);
  let formOpen = $state(false);
  let editing = $state<AssistantProvider | null>(null);
  let removing = $state<AssistantProvider | null>(null);
  let checking = $state<string | null>(null);
  let busy = $state<string | null>(null);

  async function load(): Promise<void> {
    const revision = ++loadRevision;
    const selectedScope = scope;
    try {
      const [list, cfg] = await Promise.all([
        listAssistantProviders(undefined, selectedScope),
        session.can('admin') ? getSettings() : Promise.resolve({ settings: [] }),
      ]);
      if (revision !== loadRevision) return;
      providerRevision++;
      providers = list.items ?? [];
      settings = cfg.settings ?? [];
    } catch (cause) {
      toasts.error('Could not read the assistant settings', failure(cause));
    } finally {
      if (revision === loadRevision) loading = false;
    }
  }

  $effect(() => {
    void load();
  });

  function failure(cause: unknown): string {
    return cause instanceof ApiError ? cause.message : `That could not be done. ${supportHint()}`;
  }

  const setting = (key: string) => settings.find((s) => s.key === key);
  const flag = (key: string) => setting(key)?.value === true;

  async function saveFlag(key: string, value: boolean): Promise<void> {
    try {
      const result = await updateSettings({ [key]: value } as Record<string, unknown>);
      settings = result.settings ?? settings;
      toasts.success(`${key} changed`, 'It is in force now.');
    } catch (cause) {
      toasts.error(`${key} was not changed`, failure(cause));
    }
  }

  async function check(p: AssistantProvider): Promise<void> {
    checking = p.id;
    try {
      const result = await checkAssistantProvider(p.id, scope);
      if (result.ok)
        toasts.success(`${p.name} answers`, `${result.model} in ${result.latency_ms} ms.`);
      else toasts.error(`${p.name} did not answer`, result.error ?? 'No reason was given.');
      await load();
    } catch (cause) {
      toasts.error('The test could not run', failure(cause));
    } finally {
      checking = null;
    }
  }

  async function act(
    p: AssistantProvider,
    what: () => Promise<unknown>,
    done: string,
  ): Promise<void> {
    busy = p.id;
    try {
      await what();
      toasts.success(done, p.name);
      await load();
    } catch (cause) {
      toasts.error('That could not be done', failure(cause));
    } finally {
      busy = null;
    }
  }

  async function remove(): Promise<boolean> {
    const p = removing;
    if (!p) return true;
    try {
      await deleteAssistantProvider(p.id, p.name, scope);
      toasts.success('Removed', p.name);
      removing = null;
      await load();
      return true;
    } catch (cause) {
      toasts.error(`${p.name} was not removed`, failure(cause));
      return false;
    }
  }
</script>

<PageHeader
  title="Assistant"
  subtitle="Your model for Eli conversations and PR repairs. Automatic repairs use an installation provider."
>
  <Button icon={Plus} onclick={() => ((editing = null), (formOpen = true))}>Add a provider</Button>
</PageHeader>

{#if session.can('admin') && !session.authDisabled}
  <div class="scopes" aria-label="Provider ownership">
    <Button
      variant={scope === 'personal' ? 'primary' : 'secondary'}
      onclick={() => (scope = 'personal')}>My providers</Button
    >
    <Button
      variant={scope === 'installation' ? 'primary' : 'secondary'}
      onclick={() => (scope = 'installation')}>Installation providers</Button
    >
  </div>
{/if}
<p class="scope-note">
  {scope === 'personal'
    ? 'Only your account can use these providers. Your default powers chat and repairs you request.'
    : 'Administrators manage these providers. Repository policies choose which one pays for automatic repairs.'}
</p>

{#if !loading && providers.length === 0}
  <EmptyState
    icon={MessageSquare}
    title="No model yet"
    description="Add a local server such as Ollama, or a hosted API, then test it and make it the default. Nothing leaves this machine until the assistant is asked something."
  >
    <Button icon={Plus} onclick={() => ((editing = null), (formOpen = true))}>Add a provider</Button
    >
  </EmptyState>
{:else}
  <div class="cards">
    {#each providers as p (p.id)}
      <AssistantProviderCard
        provider={p}
        checking={checking === p.id}
        busy={busy === p.id}
        oncheck={check}
        ondefault={(x) => act(x, () => setDefaultAssistantProvider(x.id, scope), 'Now the default')}
        ontoggle={(x, enabled) =>
          act(
            x,
            () => updateAssistantProvider(x.id, { enabled }, scope),
            enabled ? 'Enabled' : 'Disabled',
          )}
        onedit={(x) => ((editing = x), (formOpen = true))}
        onremove={(x) => (removing = x)}
      />
    {/each}
  </div>
{/if}

{#if !loading && scope === 'personal'}
  <AssistantChat {providers} />
{/if}

{#if session.can('admin')}
  <section class="switches" aria-labelledby="assistant-switches">
    <h3 id="assistant-switches">Where its traffic may go</h3>
    <Switch
      checked={flag(PRIVATE_KEY)}
      label="Allow a private provider address"
      description="A model on this machine or on your network. Off, saving such an address is refused."
      disabled={!setting(PRIVATE_KEY)}
      onchange={(v) => void saveFlag(PRIVATE_KEY, v)}
    />
    <Switch
      checked={flag(LOCAL_ONLY_KEY)}
      label="Local models only"
      description="Refuse any connection that resolves to a public address, so nothing the assistant is told can leave this machine or the LAN. A hosted provider cannot be reached while this is on."
      disabled={!setting(LOCAL_ONLY_KEY)}
      onchange={(v) => void saveFlag(LOCAL_ONLY_KEY, v)}
    />
  </section>
{/if}

<EliRepairs {providerRevision} />

<AssistantProviderForm
  bind:open={formOpen}
  {editing}
  {scope}
  onsaved={load}
  onclose={() => (editing = null)}
/>

<ConfirmDialog
  open={removing !== null}
  title="Remove provider"
  name={removing?.name}
  description="The assistant will no longer be able to use it, and its key is gone with it."
  requireName
  confirmLabel="Remove"
  onconfirm={remove}
  oncancel={() => (removing = null)}
/>

<style>
  .scopes {
    display: flex;
    gap: var(--z-space-2);
    flex-wrap: wrap;
  }
  .scope-note {
    color: var(--z-text-muted);
    margin-bottom: var(--z-space-5);
    font-size: var(--z-text-sm);
  }
  .cards {
    display: grid;
    gap: var(--z-space-4);
    grid-template-columns: repeat(auto-fill, minmax(20rem, 1fr));
    margin-bottom: var(--z-space-6);
  }
  .switches {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-top: var(--z-space-5);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .switches h3 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
</style>
