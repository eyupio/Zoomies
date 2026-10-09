<!--
  Adding a provider, and editing one.

  The key is write-only: an existing provider says "set, never shown" on its
  card, and the box here is empty, because a field that renders dots has
  been sent the secret to render them. Test works before Save does, against
  exactly the draft in the form, so a key is proved when it is typed.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { supportHint } from '$lib/errors';
  import {
    ApiError,
    checkAssistantDraft,
    createAssistantProvider,
    listAssistantModels,
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
  import {
    baseURLHint,
    checkSummary,
    DEFAULT_PRESET,
    isSubscriptionKind,
    KIND_LABELS,
    PRESETS,
    presetFor,
  } from './assistant';

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
  let presetId = $state(DEFAULT_PRESET);
  let baseURL = $state('');
  let model = $state('');
  let apiKey = $state('');
  let enabled = $state(true);
  let fleetAccess = $state(false);

  let kinds = $state<readonly { kind: AssistantProviderKind; default_base_url: string }[]>([]);
  // The provider's own list of models, once it has been asked. Empty means not
  // asked, or asked and none came back; the box is typed into then.
  let models = $state<readonly string[]>([]);
  let loadingModels = $state(false);
  let modelsNote = $state('');
  let saving = $state(false);
  let testing = $state(false);
  let errors = $state<Record<string, string>>({});
  let refusal = $state('');
  let tested = $state('');

  // The form starts from the provider being edited when it opens, and from nothing
  // it later reads. The reset is untracked on purpose: loading the models reads the
  // form's own fields, so tracking them made every keystroke, and every change of
  // a switch, run the reset again, which put the saved values back and asked the
  // provider for its models once more, without end.
  $effect(() => {
    if (!open) return;
    const r = editing;
    untrack(() => {
      const start = r
        ? presetFor(r.kind, r.base_url)
        : PRESETS.find((p) => p.id === DEFAULT_PRESET);
      presetId = start?.id ?? DEFAULT_PRESET;
      name = r?.name ?? start?.name ?? '';
      kind = r?.kind ?? start?.kind ?? 'openai_compatible';
      baseURL = r?.base_url ?? start?.baseURL ?? '';
      model = r?.model ?? '';
      apiKey = '';
      enabled = r ? r.enabled : true;
      fleetAccess = r ? r.fleet_access : false;
      errors = {};
      refusal = '';
      tested = '';
      models = [];
      modelsNote = '';
      // Editing a provider that already has what it needs: its list is one look away.
      if (r) void loadModels();
      void listAssistantProviderKinds()
        .then((result) => (kinds = result.items ?? []))
        .catch(() => (kinds = []));
    });
  });

  // Until the controller has said which kinds it offers, every preset is; after,
  // only those whose kind it does.
  const presetChoices = $derived.by(() => {
    const choices = PRESETS.filter(
      (p) => kinds.length === 0 || kinds.some((k) => k.kind === p.kind),
    ).map((p) => ({ value: p.id, label: p.label }));
    // The demo's built-in model is not one a person adds, but a row of it is
    // editable where the demo seeded one.
    if (editing?.kind === 'fake') choices.unshift({ value: 'fake', label: KIND_LABELS.fake });
    return choices;
  });
  const preset = $derived(PRESETS.find((p) => p.id === presetId));
  // Somebody's own subscription has no address to type and no key to hold: it is
  // used through the vendor's own tool, signed in on the controller's machine.
  const subscription = $derived(isSubscriptionKind(kind));
  // Only some tools can say which models they have; the rest are typed or left empty.
  const listsModels = $derived(!subscription || !!preset?.listsModels);
  const defaultBaseURL = $derived(kinds.find((k) => k.kind === kind)?.default_base_url ?? '');
  // The hosted kinds have an address of their own when the box is empty.
  const presetBaseURL = $derived(preset?.baseURL || defaultBaseURL);
  const modelChoices = $derived([
    { value: '', label: 'Choose a model' },
    // A saved model the list no longer has is kept, or opening Edit would drop it.
    ...(model && !models.includes(model)
      ? [{ value: model, label: `${model} (not in the list)` }]
      : []),
    ...models.map((m) => ({ value: m, label: m })),
  ]);

  /**
   * Choosing a different provider fills in what the last one had filled in and
   * leaves alone anything the person typed: an address or a name that is still
   * the previous preset's is replaced, and one that is not is theirs.
   */
  function choose(next: string): void {
    const before = PRESETS.find((p) => p.id === presetId);
    const after = PRESETS.find((p) => p.id === next);
    presetId = next;
    models = [];
    modelsNote = '';
    if (!after) return;
    kind = after.kind;
    if (name.trim() === '' || name.trim() === (before?.name ?? '')) name = after.name;
    if (after.subscription) {
      // Nothing of an address or a key carries over to something that has neither.
      baseURL = '';
      apiKey = '';
      fleetAccess = false;
      if (model.trim() === '') model = after.defaultModel ?? '';
      if (after.listsModels) void loadModels();
      return;
    }
    if (baseURL.trim() === '' || baseURL.trim() === (before?.baseURL ?? ''))
      baseURL = after.baseURL;
  }

  function draft(): Record<string, unknown> {
    const body: Record<string, unknown> = {
      name: name.trim(),
      kind,
      base_url: baseURL.trim(),
      model: model.trim(),
      enabled,
      fleet_access: fleetAccess,
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

  /**
   * Ask the provider what it serves. It is a convenience and never a gate: a
   * provider that will not say leaves the box to be typed into, with the reason
   * beside it.
   */
  async function loadModels(): Promise<void> {
    if (loadingModels || !listsModels || (!subscription && !baseURL.trim() && !presetBaseURL))
      return;
    loadingModels = true;
    modelsNote = '';
    try {
      const result = await listAssistantModels(draft());
      models = result.items ?? [];
      modelsNote = models.length === 0 ? 'The provider listed no models; type the name.' : '';
    } catch (cause) {
      models = [];
      modelsNote =
        cause instanceof ApiError ? cause.message : `That could not be done. ${supportHint()}`;
    } finally {
      loadingModels = false;
    }
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

    <Field
      id="assistant-preset"
      label="Provider"
      hint={preset?.help || undefined}
      error={errors.kind}
    >
      <Select
        id="assistant-preset"
        value={presetId}
        options={presetChoices}
        onchange={(v: string) => choose(v)}
      />
    </Field>

    {#if !subscription}
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
    {/if}

    <Field
      id="assistant-model"
      label="Model"
      hint={modelsNote ||
        (subscription
          ? (preset?.modelHint ?? '')
          : models.length > 0
            ? 'From the provider’s own list.'
            : 'As the provider names it. Load the list once the address and key are in.')}
      error={errors.model}
    >
      {#if models.length > 0}
        <Select id="assistant-model" bind:value={model} options={modelChoices} />
      {:else}
        <Input
          id="assistant-model"
          bind:value={model}
          placeholder={subscription ? 'The tool chooses' : 'llama3.1'}
          autocomplete="off"
        />
      {/if}
    </Field>
    {#if listsModels}
      <div class="models">
        <Button size="sm" loading={loadingModels} onclick={() => void loadModels()}
          >{models.length > 0 ? 'Refresh the list' : 'Load the list of models'}</Button
        >
      </div>
    {/if}

    {#if !subscription}
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
          onblur={() => {
            // The key is the last thing the list needs; ask when it has been typed.
            if (apiKey !== '' && models.length === 0) void loadModels();
          }}
        />
      </Field>
    {/if}

    <Switch bind:checked={enabled} label="The assistant may use this provider" />

    {#if !subscription}
      <Switch
        bind:checked={fleetAccess}
        label="Let Eli read this fleet through this provider"
        description="Eli can look at runners, jobs, pools and hosts to answer, and it only reads. What it reads is sent to this provider: runner, job, repository and branch names, and log excerpts when it asks for them. That stays on this network for a provider on this machine or a private one, and leaves it for a hosted one, so leave this off if you do not want that."
      />
    {/if}

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
  .models {
    margin-top: calc(var(--z-space-2) * -1);
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
