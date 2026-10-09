<!--
  Adding a provider, and editing one.

  The key is write-only: an existing provider says "set, never shown" on its
  card, and the box here is empty, because a field that renders dots has
  been sent the secret to render them. Test works before Save does, against
  exactly the draft in the form, so a key is proved when it is typed.
-->
<script lang="ts">
  import { supportHint } from '$lib/errors';
  import {
    ApiError,
    checkAssistantDraft,
    createAssistantProvider,
    listAssistantProviderKinds,
    updateAssistantProvider,
  } from '$lib/api/client';
  import type { AssistantProvider, AssistantProviderKind } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import { baseURLHint, checkSummary, KIND_LABELS } from './assistant';

  interface Props {
    open?: boolean;
    /** The provider being edited, or null when one is being added. */
    editing?: AssistantProvider | null;
    onsaved: () => void;
    onclose: () => void;
  }

  let { open = $bindable(false), editing = null, onsaved, onclose }: Props = $props();

  let name = $state('');
  let kind = $state<AssistantProviderKind>('openai_compatible');
  let baseURL = $state('');
  let model = $state('');
  let apiKey = $state('');
  let enabled = $state(true);

  let kinds = $state<readonly { kind: AssistantProviderKind; default_base_url: string }[]>([]);
  let saving = $state(false);
  let testing = $state(false);
  let errors = $state<Record<string, string>>({});
  let refusal = $state('');
  let tested = $state('');

  $effect(() => {
    if (!open) return;
    const r = editing;
    name = r?.name ?? '';
    kind = r?.kind ?? 'openai_compatible';
    baseURL = r?.base_url ?? '';
    model = r?.model ?? '';
    apiKey = '';
    enabled = r ? r.enabled : true;
    errors = {};
    refusal = '';
    tested = '';
    void listAssistantProviderKinds()
      .then((result) => (kinds = result.items ?? []))
      .catch(() => (kinds = []));
  });

  const kindChoices = $derived.by(() => {
    const choices = kinds.map((k) => ({ value: k.kind, label: KIND_LABELS[k.kind] ?? k.kind }));
    // The demo's built-in model is not a kind a person adds, but a row of
    // it is editable where the demo seeded one.
    if (editing?.kind === 'fake') choices.unshift({ value: 'fake', label: KIND_LABELS.fake });
    return choices;
  });
  const defaultBaseURL = $derived(kinds.find((k) => k.kind === kind)?.default_base_url ?? '');

  function draft(): Record<string, unknown> {
    const body: Record<string, unknown> = {
      name: name.trim(),
      kind,
      base_url: baseURL.trim(),
      model: model.trim(),
      enabled,
    };
    // Absent leaves the sealed key alone; a new provider sends what it has.
    if (apiKey !== '' || !editing) body.api_key = apiKey;
    // A draft check of a saved row borrows its sealed key when the box is blank.
    if (editing) body.id = editing.id;
    return body;
  }

  function carry(cause: unknown): void {
    if (cause instanceof ApiError) {
      errors = cause.fieldErrors();
      refusal = Object.keys(errors).length > 0 ? '' : cause.message;
      return;
    }
    refusal = `That could not be done. ${supportHint()}`;
  }

  async function test(): Promise<void> {
    testing = true;
    errors = {};
    refusal = '';
    tested = '';
    try {
      const result = await checkAssistantDraft(draft());
      tested = checkSummary(result);
      if (result.ok) toasts.success('It answers', tested);
    } catch (cause) {
      carry(cause);
    } finally {
      testing = false;
    }
  }

  async function save(): Promise<void> {
    saving = true;
    errors = {};
    refusal = '';
    try {
      if (editing) {
        await updateAssistantProvider(editing.id, draft());
        toasts.success('Saved', `${name.trim()} is as the form has it.`);
      } else {
        await createAssistantProvider(draft());
        toasts.success('Added', `${name.trim()} can be tested and set as the default.`);
      }
      onsaved();
      open = false;
    } catch (cause) {
      carry(cause);
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title={editing ? `Edit ${editing.name}` : 'Add a provider'}
  description="A model the assistant may talk to. Nothing is sent to it until you ask the assistant something; Test sends one short prompt."
  size="md"
  {onclose}
>
  <div class="form">
    <Field
      id="assistant-name"
      label="Name"
      hint="What this provider is called here."
      error={errors.name}
    >
      <Input id="assistant-name" bind:value={name} placeholder="Local Ollama" autocomplete="off" />
    </Field>

    <Field id="assistant-kind" label="Kind" error={errors.kind}>
      <Select id="assistant-kind" bind:value={kind} options={kindChoices} />
    </Field>

    <Field
      id="assistant-base-url"
      label="Base URL"
      hint={baseURLHint(kind, defaultBaseURL)}
      error={errors.base_url}
    >
      <Input
        id="assistant-base-url"
        bind:value={baseURL}
        placeholder={defaultBaseURL || 'http://localhost:11434/v1'}
        autocomplete="off"
      />
    </Field>

    <Field id="assistant-model" label="Model" hint="As the provider names it." error={errors.model}>
      <Input id="assistant-model" bind:value={model} placeholder="llama3.1" autocomplete="off" />
    </Field>

    <Field
      id="assistant-api-key"
      label="API key"
      hint={editing?.key_configured
        ? 'One is set. Type a new one to replace it, or leave this empty to keep it.'
        : 'Sealed with this fleet’s encryption key and never shown again. A local server usually needs none.'}
      error={errors.api_key}
    >
      <Input
        id="assistant-api-key"
        type="password"
        bind:value={apiKey}
        autocomplete="new-password"
      />
    </Field>

    <Switch bind:checked={enabled} label="The assistant may use this provider" />

    {#if tested}
      <p class="tested">{tested}</p>
    {/if}
    {#if refusal}
      <p class="refusal">{refusal}</p>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)} disabled={saving || testing}
      >Cancel</Button
    >
    <Button variant="secondary" onclick={test} loading={testing} disabled={saving}>Test</Button>
    <Button onclick={save} loading={saving} disabled={testing}
      >{editing ? 'Save' : 'Add provider'}</Button
    >
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .tested {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-accent);
  }
  .refusal {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-danger);
  }
</style>
