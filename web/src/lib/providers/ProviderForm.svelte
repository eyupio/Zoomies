<!--
  Adding a provider, and editing one.

  The placement step is rendered from the schema the controller serves rather
  than written out here: a driver knows which of its settings exist, which are
  required and what choosing one means, and a form that carried its own copy
  would be wrong the first time a driver gained a setting. `SettingSpec.help`
  and `SettingSpec.consequence` are the driver's own sentences and are shown as
  written.

  Three things come from the driver's description of itself rather than from
  here: what to prepare before the first question (the guide), where each
  answer is found (a help icon beside the label), and the menus discovery can
  offer once there is a credential to ask with. The same form is also shown as
  the `zoomies providers add` line it would be in a terminal, on the review
  step, because a wizard somebody wants to script is a wizard they read once.

  Two things are borrowed from the pool wizard on purpose. Numbers are held as
  strings in the draft, because '' and '0' are different answers and
  `max_machines: 0` is the setting that rents nothing. And the server is asked
  for a verdict on the draft before anything is created, from the placement step
  onward, so the answer sits beside the setting that changes it while there is
  still a reason to change it.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import {
    ApiError,
    createProvider,
    discoverProviderDraft,
    getProviderDiscovery,
    listProviderKinds,
    updateProvider,
    validateProvider,
  } from '$lib/api/client';
  import type {
    Body,
    Provider,
    ProviderChoice,
    ProviderDiscovery,
    ProviderKind,
    ProviderSetting,
    ProviderValidation,
  } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { severityStatus } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import { cpuLabel, memoryLabel, withValue } from '$lib/pools/sizing';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import Select from '$lib/components/Select.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import Wizard from '$lib/components/Wizard.svelte';
  import LabelMapEditor from '$lib/hosts/LabelMapEditor.svelte';
  import {
    applyDiscovery,
    applySettingDefaults,
    choicesFor,
    draftErrors,
    draftFromProvider,
    emptyDraft,
    FIELD_LABELS,
    normaliseEndpoint,
    providerCommand,
    stepForField,
    STEP_FIELDS,
    suggestName,
    toProviderBody,
    willRentNothing,
    WIZARD_STEPS,
  } from './draft';
  import type { ProviderDraft } from './draft';

  /* What a machine is sized in, with zero for "the template decides". The
     steps are the sizes a hypervisor template is actually built at. */
  const MACHINE_CPU_NOTCHES = [0, 1, 2, 4, 6, 8, 12, 16, 24, 32, 48, 64];
  const MACHINE_MEMORY_NOTCHES = [
    0, 1024, 2048, 4096, 8192, 16384, 24576, 32768, 49152, 65536, 98304, 131072, 262144,
  ];
  const MACHINE_DISK_NOTCHES = [
    0, 10240, 20480, 40960, 81920, 102400, 163840, 204800, 327680, 512000, 1048576, 2097152,
  ];

  interface Props {
    /** The provider being edited, or absent when one is being created. */
    provider?: Provider | null;
    oncancel: () => void;
    ondone: (provider: Provider) => void;
    class?: string;
  }

  let { provider = null, oncancel, ondone, class: className = '' }: Props = $props();

  const editing = $derived(Boolean(provider?.id));
  const tailcatAvailable = $derived(session.meta?.tailcat_available === true);
  const connectionOptions = $derived([
    {
      value: 'direct',
      label: 'Direct',
      description: 'Zoomies dials the address over the network. Works on your LAN or over HTTPS.',
    },
    {
      value: 'tailcat',
      label: 'Private connection · Tailcat',
      description: tailcatAvailable
        ? 'For a hypervisor at home with no address Zoomies can reach. Run zoomies gateway beside it and paste the address it prints.'
        : 'Private connections need authentication, a controller encryption key and server.tailcat_enabled. Ask your administrator to enable these and restart Zoomies.',
      disabled: !tailcatAvailable,
    },
  ]);

  // The draft starts from the provider being edited and is the operator's
  // from then on, so this reads the prop once on purpose.
  let draft = $state<ProviderDraft>(
    untrack(() => (provider ? draftFromProvider(provider) : emptyDraft())),
  );
  let current = $state(0);
  let touched = $state<Record<string, boolean>>({});
  let serverErrors = $state<Record<string, string>>({});
  let submitting = $state(false);
  let panel = $state<HTMLDivElement | null>(null);

  let kinds = $state<ProviderKind[]>([]);
  let kindsError = $state<unknown>(null);
  let kindsAttempt = $state(0);

  let verdict = $state<ProviderValidation | null>(null);
  let validating = $state(false);

  // What the credential can see, asked for once the connect step is done and
  // again whenever its answers change. `discoveryNote` is the sentence shown
  // when the cluster could not be asked; the boxes then take typed answers.
  let discovery = $state<ProviderDiscovery | null>(null);
  let discoveryNote = $state('');
  let discovering = $state(false);
  let discoveryAttempt = $state(0);

  const reviewStep = WIZARD_STEPS.length - 1;
  const placementStep = WIZARD_STEPS.findIndex((step) => step.id === 'placement');

  const kind = $derived(kinds.find((k) => k.kind === draft.kind) ?? null);
  const specs = $derived<ProviderSetting[]>(kind?.settings ?? []);
  const plainSpecs = $derived(specs.filter((spec) => !spec.advanced));
  const advancedSpecs = $derived(specs.filter((spec) => spec.advanced));

  const clientErrors = $derived(draftErrors(draft, specs, { editing }));
  const body = $derived(toProviderBody(draft));
  const nothingRented = $derived(willRentNothing(draft));
  const command = $derived(
    providerCommand(draft, { editing, existingName: provider?.name ?? undefined, specs }),
  );
  /** The host the credential is being asked about, for the sentences below. */
  const endpointHost = $derived.by(() => {
    try {
      return new URL(draft.endpoint).host;
    } catch {
      return draft.endpoint || 'the provider';
    }
  });
  /**
   * The connect step's answers, as one string, so discovery runs again exactly
   * when one of them changes and not on every keystroke elsewhere.
   */
  const connectKey = $derived(
    JSON.stringify([
      draft.kind,
      draft.endpoint,
      draft.credential,
      draft.ca_pem,
      draft.insecure_skip_verify,
      draft.connection,
      draft.tailcat_address,
    ]),
  );
  const connectComplete = $derived(
    Object.keys(clientErrors).every((field) => !(STEP_FIELDS[0] ?? []).includes(field)),
  );

  /** Client rules show once a field has been left; server rules show at once. */
  const errors = $derived.by(() => {
    const out: Record<string, string> = { ...serverErrors };
    for (const [field, message] of Object.entries(clientErrors)) {
      if (touched[field]) out[field] = message;
    }
    return out;
  });

  const blocking = $derived.by(() => {
    const fields =
      current === reviewStep ? Object.keys(clientErrors) : (STEP_FIELDS[current] ?? []);
    return Object.entries(clientErrors)
      .filter(([field]) => fields.includes(field) || fields.includes(settingGroup(field)))
      .map(([, message]) => message);
  });
  const canAdvance = $derived(blocking.length === 0 && !submitting);

  /** A driver setting's step key: every `settings.<key>` belongs to placement. */
  function settingGroup(field: string): string {
    return field.startsWith('settings.') ? 'settings' : field;
  }

  function touch(field: string): void {
    touched = { ...touched, [field]: true };
    if (serverErrors[field] !== undefined) {
      const rest = { ...serverErrors };
      delete rest[field];
      serverErrors = rest;
    }
  }

  function touchStep(step: number): void {
    const fields = STEP_FIELDS[step] ?? [];
    const next = { ...touched };
    for (const field of Object.keys(clientErrors)) {
      if (fields.includes(settingGroup(field))) next[field] = true;
    }
    for (const field of fields) next[field] = true;
    touched = next;
  }

  function goTo(step: number): void {
    current = Math.min(Math.max(step, 0), reviewStep);
  }

  /* -- what the controller can build ---------------------------------------- */

  $effect(() => {
    void kindsAttempt;
    const controller = new AbortController();
    listProviderKinds(controller.signal)
      .then((response) => {
        kinds = response.items ?? [];
        kindsError = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        kindsError = cause;
      });
    return () => controller.abort();
  });

  // One kind and nothing chosen is not a choice, so it is made. A form that
  // asks a question with one answer is a form with an extra click in it.
  $effect(() => {
    const only = kinds.length === 1 ? kinds[0] : undefined;
    if (!only || untrack(() => draft.kind) !== '') return;
    draft.kind = only.kind;
  });

  // Defaults the driver published, filled in as soon as its schema arrives and
  // never over an answer already given.
  $effect(() => {
    const list = specs;
    if (list.length === 0) return;
    const next = applySettingDefaults(
      untrack(() => draft),
      list,
    );
    if (next !== untrack(() => draft)) draft = next;
  });

  /* -- what the credential can see ------------------------------------------- */

  $effect(() => {
    void discoveryAttempt;
    const key = connectKey;
    if (current < placementStep || !connectComplete || !kind?.can_discover) return;
    // An edit with the box left blank keeps the sealed credential, so the
    // saved provider is the one to ask; a draft, or an edit with a new
    // credential typed, is asked about as it stands.
    const payload = body;
    const existingID = provider?.id;
    const useSaved = editing && existingID && draft.credential.trim() === '';
    const controller = new AbortController();
    discovering = true;
    discoveryNote = '';
    void key;
    const request = useSaved
      ? getProviderDiscovery(existingID, controller.signal)
      : discoverProviderDraft(payload, controller.signal);
    request
      .then((found) => {
        discovery = found;
        if (found.unavailable) {
          discoveryNote = found.unavailable;
          return;
        }
        const next = applyDiscovery(
          untrack(() => draft),
          untrack(() => specs),
          found,
        );
        if (next !== untrack(() => draft)) draft = next;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        discovery = null;
        discoveryNote =
          cause instanceof ApiError
            ? cause.message
            : 'The provider could not be asked what it can see.';
      })
      .finally(() => {
        discovering = false;
      });
    return () => controller.abort();
  });

  /* -- the server's verdict, before anything is created --------------------- */

  $effect(() => {
    if (current < placementStep) return;
    const payload = body;
    const controller = new AbortController();
    validating = true;
    const timer = setTimeout(() => {
      validateProvider(payload, provider?.id, controller.signal)
        .then((result) => {
          verdict = result;
        })
        .catch((cause: unknown) => {
          if (cause instanceof DOMException && cause.name === 'AbortError') return;
          // The verdict is guidance, not the gate. Losing it is not worth an
          // error state over a form the operator can still submit.
        })
        .finally(() => {
          validating = false;
        });
    }, 250);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  });

  /* -- focus follows the step ------------------------------------------------ */

  let lastStep = -1;
  $effect(() => {
    const step = current;
    if (step === lastStep) return;
    const moved = lastStep !== -1;
    lastStep = step;
    if (moved) untrack(() => panel)?.focus();
  });

  /* -- submitting ------------------------------------------------------------ */

  function applyFieldErrors(cause: ApiError): void {
    const fields = cause.fieldErrors();
    serverErrors = fields;
    const first = Object.keys(fields)[0];
    if (first !== undefined) goTo(stepForField(first));
  }

  async function finish(): Promise<void> {
    if (submitting) return;
    touchStep(current);
    const outstanding = Object.keys(clientErrors);
    if (outstanding.length > 0) {
      const next = { ...touched };
      for (const field of outstanding) next[field] = true;
      touched = next;
      goTo(stepForField(outstanding[0] ?? 'name'));
      return;
    }
    submitting = true;
    try {
      const payload = toProviderBody(draft, { complete: editing });
      const saved =
        provider && provider.id
          ? await updateProvider(provider.id, payload as Body<'updateProvider'>)
          : await createProvider(payload);
      toasts.success(
        editing ? `Saved ${payload.name}` : `Added ${payload.name}`,
        (payload.max_machines ?? 0) === 0
          ? 'Nothing is rented yet: its ceiling is zero. Raise it when you are ready to spend.'
          : 'Run the check before the first machine is built.',
      );
      ondone(saved);
    } catch (cause) {
      if (cause instanceof ApiError) applyFieldErrors(cause);
      toasts.fromError(
        cause,
        editing ? 'The provider was not saved' : 'The provider was not added',
      );
    } finally {
      submitting = false;
    }
  }

  /* -- filling the connect step in ------------------------------------------- */

  /**
   * On leaving the address box: give it the scheme and port it was typed
   * without, and name the provider after the host when nobody has named it.
   */
  function leaveEndpoint(): void {
    draft.endpoint = normaliseEndpoint(draft.endpoint, kind?.endpoint_example);
    if (draft.name.trim() === '' && !touched.name && draft.endpoint !== '')
      draft.name = suggestName(draft.endpoint, draft.kind || 'provider');
    touch('endpoint');
  }

  /** Whether the connect step has everything discovery needs to ask with. */
  const canDiscover = $derived(connectComplete && Boolean(kind?.can_discover));

  /* -- rendering one driver setting ------------------------------------------ */

  function settingValue(key: string): string {
    return draft.settings[key] ?? '';
  }

  function setSetting(key: string, value: string): void {
    draft.settings = { ...draft.settings, [key]: value };
  }

  /** A list setting is comma-separated in the draft; the checkboxes edit it as a set. */
  function listHas(key: string, value: string): boolean {
    return settingValue(key)
      .split(',')
      .map((v) => v.trim())
      .includes(value);
  }

  function toggleListValue(key: string, value: string, on: boolean): void {
    const current = settingValue(key)
      .split(',')
      .map((v) => v.trim())
      .filter((v) => v !== '' && v !== value);
    if (on) current.push(value);
    setSetting(key, current.join(','));
    touch(`settings.${key}`);
  }

  function choiceLabel(choice: ProviderChoice): string {
    return choice.label && choice.label !== choice.value
      ? `${choice.label} (${choice.value})`
      : choice.value;
  }

  /** What picking the current answer means, in the driver's words. */
  function chosenConsequence(spec: ProviderSetting): string {
    const value = settingValue(spec.key);
    return choicesFor(spec, discovery).find((c) => c.value === value)?.consequence ?? '';
  }

  const OS_OPTIONS = [
    { value: '', label: 'Any' },
    { value: 'ubuntu', label: 'Ubuntu' },
    { value: 'debian', label: 'Debian' },
    { value: 'fedora', label: 'Fedora' },
    { value: 'rocky', label: 'Rocky Linux' },
  ];
  const ARCH_OPTIONS = [
    { value: '', label: 'Any' },
    { value: 'amd64', label: 'amd64' },
    { value: 'arm64', label: 'arm64' },
  ];
  const BACKEND_OPTIONS = [
    { value: 'docker', label: 'Docker' },
    { value: 'podman', label: 'Podman' },
    { value: 'process', label: 'Process' },
  ];

  // The editor owns its rows from here: a row with an empty key is a row
  // half typed, which the map the draft holds cannot represent.
  let labelRows = $state(
    untrack(() => Object.entries(draft.machine_labels).map(([key, value]) => ({ key, value }))),
  );
  $effect(() => {
    const map: Record<string, string> = {};
    for (const row of labelRows) {
      if (row.key.trim() === '') continue;
      map[row.key.trim()] = row.value;
    }
    draft.machine_labels = map;
  });
</script>

{#snippet setting(spec: ProviderSetting)}
  {@const choices = choicesFor(spec, discovery)}
  <Field
    label={spec.label || spec.key}
    hint={spec.help}
    help={spec.source}
    notice={chosenConsequence(spec) || spec.consequence}
    error={errors[`settings.${spec.key}`]}
    required={spec.required}
  >
    {#snippet children({ id, describedBy, invalid })}
      {#if choices.length > 0 && spec.kind === 'list'}
        <!-- Discovery listed them, so they are chosen rather than typed. -->
        <div class="choice-list" role="group" aria-labelledby="{id}-legend">
          <span class="sr-only" id="{id}-legend">{spec.label || spec.key}</span>
          {#each choices as choice (choice.value)}
            <Checkbox
              checked={listHas(spec.key, choice.value)}
              label={choiceLabel(choice)}
              description={choice.consequence}
              onchange={(on) => toggleListValue(spec.key, choice.value, on)}
            />
          {/each}
        </div>
      {:else if choices.length > 0 && (spec.kind === 'choice' || spec.kind === 'text')}
        <Select
          bind:value={() => settingValue(spec.key), (next) => setSetting(spec.key, next)}
          {id}
          {describedBy}
          {invalid}
          placeholder={spec.required ? 'Choose one' : 'None'}
          options={choices.map((c) => ({ value: c.value, label: choiceLabel(c) }))}
          onchange={() => touch(`settings.${spec.key}`)}
        />
      {:else if spec.kind === 'bool'}
        <Checkbox
          checked={settingValue(spec.key) === 'true'}
          label={spec.label || spec.key}
          ariaLabel={spec.label || spec.key}
          onchange={(checked) => {
            setSetting(spec.key, checked ? 'true' : 'false');
            touch(`settings.${spec.key}`);
          }}
        />
      {:else}
        <!-- A function binding rather than a field: the answers live in one
             map keyed by the driver's own setting names, and a map cannot be
             bound to a control by path. -->
        <Input
          bind:value={() => settingValue(spec.key), (next) => setSetting(spec.key, next)}
          {id}
          {describedBy}
          {invalid}
          type={spec.kind === 'secret' ? 'password' : 'text'}
          inputmode={spec.kind === 'number' ? 'numeric' : undefined}
          mono={spec.kind === 'list'}
          placeholder={spec.default}
          onblur={() => touch(`settings.${spec.key}`)}
        />
      {/if}
    {/snippet}
  </Field>
{/snippet}

<Wizard
  class={className}
  steps={WIZARD_STEPS}
  bind:current
  {canAdvance}
  busy={submitting}
  finishLabel={editing ? 'Save changes' : 'Add provider'}
  cancelLabel="Cancel"
  onnext={() => touchStep(current)}
  onback={() => touchStep(current)}
  onfinish={finish}
  {oncancel}
>
  {#snippet children(step)}
    <div class="step" bind:this={panel} tabindex="-1" role="group" aria-label={step.title}>
      {#if step.id === 'connect'}
        {#if kindsError}
          <ErrorState
            error={kindsError}
            title="The provider kinds could not be read"
            onretry={() => (kindsAttempt += 1)}
          />
        {/if}
        {#if kind && (kind.guide?.length ?? 0) > 0}
          <!-- Before the first question: every one of these is done somewhere
               other than this form, and asking for the result without saying
               how to get it is how a wizard gets abandoned. Open for a new
               provider; a provider being edited has been through it. -->
          <details class="guide" open={!editing}>
            <summary>Before you start: what {kind.label || kind.kind} needs from you</summary>
            <ol class="guide-steps">
              {#each kind.guide ?? [] as step, i (step.title)}
                <li>
                  <p class="guide-title">{step.title}</p>
                  {#if step.detail}<p class="guide-detail">{step.detail}</p>{/if}
                  {#if step.command}
                    <div class="command small">
                      <pre class="mono"><code>{step.command}</code></pre>
                      <div class="command-actions">
                        <CopyButton
                          value={step.command}
                          label="Copy the commands for step {i + 1}"
                          showLabel
                        />
                      </div>
                    </div>
                  {/if}
                </li>
              {/each}
            </ol>
          </details>
        {/if}

        <Field
          label="Kind"
          hint="Which infrastructure this rents machines from."
          error={errors.kind}
          required
        >
          {#snippet children({ id, describedBy, invalid })}
            <Select
              bind:value={draft.kind}
              {id}
              {describedBy}
              {invalid}
              placeholder="Choose one"
              options={kinds.map((k) => ({ value: k.kind, label: k.label || k.kind }))}
              onchange={() => touch('kind')}
            />
          {/snippet}
        </Field>

        <Field
          label="Name"
          hint="What you will call it on this page and in a log line."
          error={errors.name}
          required
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.name}
              {id}
              {describedBy}
              {invalid}
              placeholder="proxmox-lab"
              onblur={() => touch('name')}
            />
          {/snippet}
        </Field>

        <Field
          label="Address"
          hint="Where Zoomies reaches it. A bare host name gets the scheme and the port filled in."
          help={kind?.endpoint_source}
          error={errors.endpoint}
          required
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.endpoint}
              {id}
              {describedBy}
              {invalid}
              type="url"
              mono
              placeholder={kind?.endpoint_example || 'https://provider.example.com'}
              onblur={leaveEndpoint}
            />
          {/snippet}
        </Field>

        <RadioGroup
          bind:value={draft.connection}
          name="provider-connection"
          legend="Connection"
          options={connectionOptions}
          onchange={() => touch('connection')}
        />
        {#if errors.connection}
          <p class="note bad" role="alert">{errors.connection}</p>
        {/if}

        {#if draft.connection === 'tailcat'}
          <Field
            label="Private connection address"
            hint={draft.tailcat_configured
              ? 'Stored, sealed with the instance key. Leave this empty to keep it, or paste a new one if the gateway was started with a new identity.'
              : 'What zoomies gateway printed. Sealed like the credential: anyone holding it can open connections to the hypervisor, so it is never shown again.'}
            error={errors.tailcat_address}
            required={!draft.tailcat_configured}
          >
            {#snippet children({ id, describedBy, invalid })}
              <Input
                bind:value={draft.tailcat_address}
                {id}
                {describedBy}
                {invalid}
                type="password"
                mono
                autocomplete="off"
                placeholder={draft.tailcat_configured ? 'Unchanged' : 'tc…'}
                onblur={() => touch('tailcat_address')}
              />
            {/snippet}
          </Field>
          <p class="prose">
            The address above stays what the certificate is checked against; the gateway forwards
            the connection to it and reads nothing. Run
            <code>zoomies gateway --target &lt;hypervisor-ip&gt;:8006</code> on a machine that can reach
            the hypervisor, and keep it running.
          </p>
        {/if}

        <Field
          label="Credential"
          hint={editing
            ? 'Sealed in the database and never shown again. Leave this empty to keep the stored one.'
            : 'Sealed with the instance key. It is never returned, never logged, and never reaches a guest.'}
          help={kind?.credential_source}
          error={errors.credential}
          required={!editing}
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.credential}
              {id}
              {describedBy}
              {invalid}
              type="password"
              mono
              autocomplete="off"
              placeholder={editing ? 'Unchanged' : 'user@pve!token=uuid'}
              onblur={() => touch('credential')}
            />
          {/snippet}
        </Field>

        <Field
          label="Certificate authority"
          hint="The certificate to trust for this address. Leave empty to use the system trust store."
          help={kind?.ca_source}
        >
          {#snippet children({ id, describedBy })}
            <Textarea
              bind:value={draft.ca_pem}
              {id}
              {describedBy}
              rows={3}
              placeholder="-----BEGIN CERTIFICATE-----"
            />
          {/snippet}
        </Field>

        <Switch
          bind:checked={draft.insecure_skip_verify}
          label="Do not verify the certificate"
          description="The credential then travels to whatever answers at that address. A homelab hypervisor's certificate is usually its own, which is why this exists rather than being refused — but pasting the certificate above is better."
        />
      {:else if step.id === 'placement'}
        {#if kind?.can_discover}
          <div class="discovery" aria-live="polite">
            {#if discovering}
              <p class="note">Asking {endpointHost} what this credential can see…</p>
            {:else if discoveryNote}
              <p class="note">
                <strong>{endpointHost} could not be asked what it has.</strong>
                {discoveryNote}
                Type the identifiers below; the check after saving confirms them against the cluster.
              </p>
              <Button size="sm" variant="secondary" onclick={() => (discoveryAttempt += 1)}>
                Ask again
              </Button>
            {:else if discovery}
              <p class="note ok">
                Filled in from {endpointHost}: what this credential can see is offered below, and
                anything with one answer is already chosen.
              </p>
            {:else if !canDiscover}
              <p class="note">
                Finish the Connect step and the nodes, storages, bridges and templates are offered
                as a menu here instead of typed.
              </p>
            {/if}
          </div>
        {/if}
        {#if specs.length === 0}
          <p class="prose">
            This driver has no settings of its own. Everything it needs is on the other steps.
          </p>
        {:else}
          {#each plainSpecs as spec (spec.key)}
            {@render setting(spec)}
          {/each}
          {#if advancedSpecs.length > 0}
            <details class="advanced">
              <summary>Advanced settings ({advancedSpecs.length})</summary>
              <div class="advanced-body">
                {#each advancedSpecs as spec (spec.key)}
                  {@render setting(spec)}
                {/each}
              </div>
            </details>
          {/if}
        {/if}
      {:else if step.id === 'machine'}
        <p class="prose">
          One shape, and every machine this provider buys is it. A second shape is a second
          provider, which is also how two ceilings and two credentials are kept apart.
        </p>

        <Field
          label="Runner slots per machine"
          hint="How many runners one of these may carry at once."
          error={errors.machine_capacity}
          required
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.machine_capacity}
              {id}
              {describedBy}
              {invalid}
              type="number"
              min={1}
              onblur={() => touch('machine_capacity')}
            />
          {/snippet}
        </Field>

        <Field label="Backend" hint="How runners are made on the machine once it is a host.">
          {#snippet children({ id, describedBy })}
            <Select
              bind:value={draft.machine_backend}
              {id}
              {describedBy}
              options={BACKEND_OPTIONS}
            />
          {/snippet}
        </Field>

        <!--
          The machine's size is three sliders, each with a field that reads
          what a person writes -- 8g, 32768 MB, 100 GB -- because a hypervisor
          template is sized in whichever unit its author thought in. Nothing
          on the slider, and an empty field, is the template's own size.
        -->
        <Field label="vCPUs" hint="Empty means the template decides." error={errors.machine_cpus}>
          {#snippet children({ id, describedBy, invalid })}
            <QuantityField
              {id}
              quantity="cpus"
              whole
              values={withValue(MACHINE_CPU_NOTCHES, Number(draft.machine_cpus) || 0)}
              value={Number(draft.machine_cpus) || 0}
              label="vCPUs"
              valuetext={(v) => (v === 0 ? 'the template decides' : cpuLabel(v))}
              marks={[{ value: 0, label: 'the template' }]}
              empty={{ value: 0, placeholder: 'the template decides' }}
              {describedBy}
              {invalid}
              onchange={(v) => {
                draft.machine_cpus = v ? String(v) : '';
                touch('machine_cpus');
              }}
            />
          {/snippet}
        </Field>
        <Field label="Memory" error={errors.machine_memory_mb}>
          {#snippet children({ id, describedBy, invalid })}
            <QuantityField
              {id}
              quantity="mb"
              values={withValue(MACHINE_MEMORY_NOTCHES, Number(draft.machine_memory_mb) || 0)}
              value={Number(draft.machine_memory_mb) || 0}
              label="Memory"
              valuetext={(v) => (v === 0 ? 'the template decides' : memoryLabel(v))}
              marks={[{ value: 0, label: 'the template' }]}
              empty={{ value: 0, placeholder: 'the template decides' }}
              {describedBy}
              {invalid}
              onchange={(v) => {
                draft.machine_memory_mb = v ? String(v) : '';
                touch('machine_memory_mb');
              }}
            />
          {/snippet}
        </Field>
        <Field label="Disk" error={errors.machine_disk_mb}>
          {#snippet children({ id, describedBy, invalid })}
            <QuantityField
              {id}
              quantity="mb"
              values={withValue(MACHINE_DISK_NOTCHES, Number(draft.machine_disk_mb) || 0)}
              value={Number(draft.machine_disk_mb) || 0}
              label="Disk"
              valuetext={(v) => (v === 0 ? 'the template decides' : memoryLabel(v))}
              marks={[{ value: 0, label: 'the template' }]}
              empty={{ value: 0, placeholder: 'the template decides' }}
              {describedBy}
              {invalid}
              onchange={(v) => {
                draft.machine_disk_mb = v ? String(v) : '';
                touch('machine_disk_mb');
              }}
            />
          {/snippet}
        </Field>

        <div class="row">
          <Field label="Operating system" hint="What a pool's platform will match against.">
            {#snippet children({ id, describedBy })}
              <Select bind:value={draft.machine_os} {id} {describedBy} options={OS_OPTIONS} />
            {/snippet}
          </Field>
          <Field label="Version">
            {#snippet children({ id, describedBy })}
              <Input bind:value={draft.machine_os_version} {id} {describedBy} placeholder="24.04" />
            {/snippet}
          </Field>
          <Field label="Architecture">
            {#snippet children({ id, describedBy })}
              <Select bind:value={draft.machine_arch} {id} {describedBy} options={ARCH_OPTIONS} />
            {/snippet}
          </Field>
        </div>

        <Field
          label="Machine labels"
          hint="What a host made from this machine answers to. Pools select hosts by these."
        >
          {#snippet children({ describedBy })}
            <LabelMapEditor bind:rows={labelRows} {describedBy} />
          {/snippet}
        </Field>
      {:else if step.id === 'limits'}
        <Field
          label="Maximum machines"
          hint="The ceiling. Zero rents nothing, which is what a provider starts at."
          error={errors.max_machines}
          required
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.max_machines}
              {id}
              {describedBy}
              {invalid}
              type="number"
              min={0}
              onblur={() => touch('max_machines')}
            />
          {/snippet}
        </Field>

        <Field
          label="Machines built at once"
          hint="A hypervisor cloning four templates at once is slower at all four than it would have been at one."
          error={errors.max_creates_in_flight}
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.max_creates_in_flight}
              {id}
              {describedBy}
              {invalid}
              type="number"
              min={1}
              onblur={() => touch('max_creates_in_flight')}
            />
          {/snippet}
        </Field>

        <Field
          label="Idle timeout"
          hint="How long a machine may sit with nothing to do before it is drained and deleted."
          error={errors.idle_timeout}
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.idle_timeout}
              {id}
              {describedBy}
              {invalid}
              placeholder="15m"
              onblur={() => touch('idle_timeout')}
            />
          {/snippet}
        </Field>

        <Field
          label="Cost per machine-hour"
          hint="What one of these costs you, if you know. Shown on the machine page; nothing is decided by it."
          error={errors.cost_per_machine_hour}
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.cost_per_machine_hour}
              {id}
              {describedBy}
              {invalid}
              type="number"
              min={0}
              onblur={() => touch('cost_per_machine_hour')}
            />
          {/snippet}
        </Field>

        <Switch
          bind:checked={draft.enabled}
          label="Enabled"
          description="A disabled provider builds nothing. What it already owns is still drained and deleted."
        />

        {#if nothingRented}
          <p class="note">{nothingRented}</p>
        {/if}
      {:else}
        <dl class="summary">
          <div>
            <dt>Kind</dt>
            <dd>{kind?.label || draft.kind || 'Not chosen'}</dd>
          </div>
          <div>
            <dt>Name</dt>
            <dd>{draft.name || 'Not set'}</dd>
          </div>
          <div>
            <dt>Address</dt>
            <dd class="mono">{draft.endpoint || 'Not set'}</dd>
          </div>
          <div>
            <dt>Connection</dt>
            <dd>{draft.connection === 'tailcat' ? 'Private · Tailcat' : 'Direct'}</dd>
          </div>
          <div>
            <dt>Machine</dt>
            <dd>
              {pluralise(Number(draft.machine_capacity) || 0, 'runner slot')} · {draft.machine_backend}
            </dd>
          </div>
          <div>
            <dt>Ceiling</dt>
            <dd>{pluralise(Number(draft.max_machines) || 0, 'machine')}</dd>
          </div>
        </dl>

        <section class="terminal" aria-label="The same thing from a terminal">
          <p class="prose">
            {editing ? 'The same change' : 'The same provider'}, as one command for a shell that can
            reach Zoomies — for a setup you would rather keep in a script. It asks for the
            credential itself, so nothing secret is in the line.
          </p>
          <div class="command">
            <pre class="mono"><code>{command}</code></pre>
            <div class="command-actions">
              <CopyButton value={command} label="Copy the command" size="md" showLabel />
            </div>
          </div>
        </section>

        <section class="verdict" aria-live="polite" aria-label="What the controller makes of it">
          {#if validating && !verdict}
            <p class="note">Asking the controller…</p>
          {:else if verdict}
            {#if verdict.valid}
              <p class="note ok">
                The controller accepts this. Nothing has been created: adding it stores the
                configuration, and the check afterwards is what asks the provider itself.
              </p>
            {:else}
              <p class="note bad">
                {pluralise(verdict.errors?.length ?? 0, 'answer')} still
                {(verdict.errors?.length ?? 0) === 1 ? 'needs' : 'need'} fixing.
              </p>
              <ul class="rejected">
                {#each verdict.errors ?? [] as rejected (rejected.field)}
                  <li>
                    <!-- The API's own field name, in the words the form uses
                         for it. An operator should never have to work out that
                         "max_creates_in_flight" is the box they just filled. -->
                    <strong>{FIELD_LABELS[rejected.field ?? ''] ?? rejected.field}</strong>
                    {rejected.message}
                  </li>
                {/each}
              </ul>
            {/if}
            {#if (verdict.warnings?.length ?? 0) > 0}
              <ul class="warnings">
                {#each verdict.warnings ?? [] as warning (warning.code)}
                  <li>
                    <Badge status={severityStatus(warning.severity)} size="sm" />
                    <div>
                      <p class="title">{warning.title}</p>
                      {#if warning.detail}<p class="detail">
                          <RemedyText text={warning.detail} />
                        </p>{/if}
                    </div>
                  </li>
                {/each}
              </ul>
            {/if}
          {/if}
        </section>
      {/if}
    </div>
  {/snippet}
</Wizard>

<style>
  .step {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    outline: none;
  }
  .row {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--z-space-3);
  }
  .prose {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .guide {
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .guide summary {
    padding: var(--z-space-2) var(--z-space-3);
    cursor: pointer;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .guide-steps {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin: 0;
    padding: var(--z-space-3) var(--z-space-3) var(--z-space-4) var(--z-space-6);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .guide-steps li {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .guide-title {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .guide-detail {
    margin: 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .command {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .command.small {
    padding: var(--z-space-3);
    border-color: var(--z-border);
    background: var(--z-surface-sunken);
  }
  .command pre {
    margin: 0;
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    color: var(--z-text);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .command-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3);
  }
  .discovery {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  .choice-list {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .terminal {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .advanced {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
  }
  .advanced summary {
    padding: var(--z-space-2) var(--z-space-3);
    cursor: pointer;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .advanced-body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding: var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .note {
    margin: 0;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .note.ok {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
  }
  .note.bad {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .summary {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
  }
  .summary div {
    display: grid;
    grid-template-columns: 12rem minmax(0, 1fr);
    gap: var(--z-space-3);
  }
  dt {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .verdict {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .rejected {
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
    margin: 0;
    padding: 0 0 0 var(--z-space-4);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .warnings {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .warnings li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  .title {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .detail {
    margin: var(--z-nudge-1) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  @media (max-width: 768px) {
    .row {
      grid-template-columns: 1fr;
    }
    .summary div {
      grid-template-columns: 1fr;
      gap: var(--z-nudge-1);
    }
  }
</style>
