<!--
  Connecting Zoomies to GitHub.

  The App manifest flow is three moves, and each one is explained as it happens
  because the operator is being sent to another site in the middle of it:

    1. Zoomies builds a manifest that asks for exactly the permissions it needs.
    2. The browser posts it to GitHub, which creates the App and hands back a
       code; the exchange turns that code into credentials Zoomies keeps sealed.
    3. The operator installs the App on the organisation, and the installation
       ID that produces is what finally records the connection here.

  There is a second way in for an App that already exists, because an operator
  with a key and an installation ID should not have to make a new App.

  The stepper is hand-rolled rather than the Wizard component: advancing has to
  wait on a request that can fail, and Wizard advances as soon as its handler
  resolves.
-->
<script lang="ts">
  import { ExternalLink } from '@lucide/svelte';
  import {
    ApiError,
    createAppManifest,
    createInstallation,
    exchangeAppManifest,
  } from '$lib/api/client';
  import type { TargetType } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import Textarea from '$lib/components/Textarea.svelte';

  interface Props {
    open?: boolean;
    /** A code GitHub redirected back with, when the operator landed here rather than pasting. */
    initialCode?: string;
    oncreated?: () => void;
    onclose?: () => void;
  }

  let { open = $bindable(false), initialCode = '', oncreated, onclose }: Props = $props();

  const STEPS = [
    { id: 'describe', title: 'Describe the App' },
    { id: 'create', title: 'Create it on GitHub' },
    { id: 'install', title: 'Install it' },
  ];

  let tab = $state('manifest');
  let step = $state(0);
  let busy = $state(false);
  let errors = $state<Record<string, string>>({});
  let failure = $state('');

  /* -- what the operator types ---------------------------------------------- */

  let target = $state('');
  /** Plain strings, because RadioGroup binds a string; narrowed when the API is called. */
  let targetType = $state('org');
  let apiBase = $state('');
  let appName = $state('');
  let code = $state('');
  let installationId = $state('');

  /* -- what the server hands back -------------------------------------------- */

  let postUrl = $state('');
  let manifest = $state('');
  let manifestState = $state('');
  let appId = $state<number | null>(null);
  let appSlug = $state('');
  let installUrl = $state('');

  /* -- the manual path -------------------------------------------------------- */

  let manualAppId = $state('');
  let manualInstallationId = $state('');
  let manualTarget = $state('');
  let manualTargetType = $state('org');
  let manualApiBase = $state('');
  let manualKey = $state('');
  let manualSecret = $state('');

  $effect(() => {
    if (!open) return;
    if (initialCode && !code) {
      code = initialCode;
      step = 1;
    }
  });

  function reset(): void {
    step = 0;
    busy = false;
    errors = {};
    failure = '';
    target = '';
    targetType = 'org';
    apiBase = '';
    appName = '';
    code = '';
    installationId = '';
    postUrl = '';
    manifest = '';
    manifestState = '';
    appId = null;
    appSlug = '';
    installUrl = '';
    // The private key is a secret this component was trusted with for one
    // request. It does not linger in memory afterwards.
    manualAppId = '';
    manualInstallationId = '';
    manualTarget = '';
    manualTargetType = 'org';
    manualApiBase = '';
    manualKey = '';
    manualSecret = '';
  }

  function close(): void {
    open = false;
    onclose?.();
    reset();
  }

  function report(cause: unknown, fallback: string): void {
    if (cause instanceof ApiError) {
      errors = cause.fieldErrors();
      failure = cause.message;
    } else {
      failure = fallback;
    }
  }

  /* -- step one: build the manifest ------------------------------------------- */

  const targetError = $derived(
    target.trim() === ''
      ? ''
      : targetType === 'repo' && !target.includes('/')
        ? 'A repository target is written owner/repo.'
        : targetType === 'org' && target.includes('/')
          ? 'An organisation target is just its name, with no slash.'
          : '',
  );

  async function buildManifest(): Promise<void> {
    if (!target.trim() || targetError) return;
    busy = true;
    errors = {};
    failure = '';
    try {
      const result = await createAppManifest({
        name: appName.trim() || undefined,
        target: target.trim(),
        target_type: targetType as TargetType,
        api_base_url: apiBase.trim() || undefined,
      });
      postUrl = result.post_url ?? '';
      manifest = result.manifest ?? '';
      manifestState = result.state ?? '';
      step = 1;
    } catch (cause) {
      report(cause, 'The manifest could not be built.');
    } finally {
      busy = false;
    }
  }

  /**
   * Post the manifest to GitHub in a new tab.
   *
   * It has to be a real form submission -- GitHub reads `manifest` as a form
   * field -- and the state is appended here because the API returns the URL and
   * the state separately.
   */
  function sendToGitHub(): void {
    if (!postUrl || !manifest) return;
    const url = new URL(postUrl);
    if (manifestState) url.searchParams.set('state', manifestState);

    const form = document.createElement('form');
    form.method = 'POST';
    form.action = url.toString();
    form.target = '_blank';
    form.rel = 'noopener';
    const field = document.createElement('input');
    field.type = 'hidden';
    field.name = 'manifest';
    field.value = manifest;
    form.append(field);
    document.body.append(form);
    form.submit();
    form.remove();
  }

  /* -- step two: exchange the code -------------------------------------------- */

  async function exchange(): Promise<void> {
    if (!code.trim()) return;
    busy = true;
    errors = {};
    failure = '';
    try {
      const result = await exchangeAppManifest({
        code: code.trim(),
        state: manifestState || undefined,
        api_base_url: apiBase.trim() || undefined,
      });
      appId = result.app_id ?? null;
      appSlug = result.slug ?? result.name ?? '';
      installUrl = result.install_url ?? '';
      step = 2;
    } catch (cause) {
      report(cause, 'That code could not be exchanged.');
    } finally {
      busy = false;
    }
  }

  /* -- step three: record the installation ------------------------------------- */

  async function record(): Promise<void> {
    const id = Number(installationId.trim());
    if (!appId || !Number.isInteger(id) || id <= 0) return;
    busy = true;
    errors = {};
    failure = '';
    try {
      await createInstallation({
        app_id: appId,
        installation_id: id,
        target: target.trim(),
        target_type: targetType as TargetType,
        // Empty means "whatever this controller is configured to talk to".
        api_base_url: apiBase.trim(),
        // The private key is already held, sealed, from the exchange.
        private_key: '',
      });
      toasts.success(
        `Connected ${target.trim()}`,
        'Zoomies can now create runners for this target.',
      );
      oncreated?.();
      close();
    } catch (cause) {
      report(cause, 'The installation could not be recorded.');
    } finally {
      busy = false;
    }
  }

  /* -- the manual path --------------------------------------------------------- */

  async function connectExisting(): Promise<void> {
    busy = true;
    errors = {};
    failure = '';
    try {
      await createInstallation({
        app_id: Number(manualAppId.trim()),
        installation_id: Number(manualInstallationId.trim()),
        target: manualTarget.trim(),
        target_type: manualTargetType as TargetType,
        api_base_url: manualApiBase.trim(),
        private_key: manualKey,
        webhook_secret: manualSecret || undefined,
      });
      toasts.success(
        `Connected ${manualTarget.trim()}`,
        'Zoomies can now create runners for this target.',
      );
      oncreated?.();
      close();
    } catch (cause) {
      report(cause, 'That App could not be connected.');
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Connect GitHub" size="lg" onclose={close}>
  <Tabs
    bind:value={tab}
    label="How to connect"
    tabs={[
      { id: 'manifest', label: 'Create a new App' },
      { id: 'existing', label: 'Use an App you already have' },
    ]}
  >
    {#snippet children(active)}
      {#if active === 'manifest'}
        <ol class="steps">
          {#each STEPS as s, index (s.id)}
            <li class:done={index < step} class:active={index === step}>
              <span class="marker" aria-hidden="true">{index + 1}</span>
              <span aria-current={index === step ? 'step' : undefined}>{s.title}</span>
            </li>
          {/each}
        </ol>

        <div class="stack">
          {#if step === 0}
            <p class="lede">
              Zoomies builds a GitHub App manifest that asks for exactly the permissions it needs,
              and nothing more. Nothing is created until you confirm it on GitHub.
            </p>

            <Field
              label="Organisation or repository"
              hint="The account whose runners this App will manage."
              error={errors.target ?? targetError}
            >
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={target}
                  {id}
                  {describedBy}
                  invalid={invalid || Boolean(targetError)}
                  placeholder={targetType === 'repo' ? 'acme/widgets' : 'acme'}
                  mono
                />
              {/snippet}
            </Field>

            <RadioGroup
              bind:value={targetType}
              name="target-type"
              legend="Target type"
              inline
              options={[
                { value: 'org', label: 'Organisation' },
                { value: 'repo', label: 'A single repository' },
              ]}
            />

            <Field
              label="App name"
              hint="Optional. Defaults to a name that includes the target, and must be unique across GitHub."
              error={errors.name}
            >
              {#snippet children({ id, describedBy, invalid })}
                <Input bind:value={appName} {id} {describedBy} {invalid} placeholder="Zoomies" />
              {/snippet}
            </Field>

            <Field
              label="API base URL"
              hint="Optional. Leave empty for github.com; set it for GitHub Enterprise Server."
              error={errors.api_base_url}
            >
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={apiBase}
                  {id}
                  {describedBy}
                  {invalid}
                  type="url"
                  mono
                  placeholder="https://api.github.com"
                />
              {/snippet}
            </Field>
          {:else if step === 1}
            <p class="lede">
              The next button opens GitHub in a new tab with the manifest already filled in. Confirm
              it there; GitHub creates the App and sends the browser back here with a code in the
              address bar.
            </p>

            <div>
              <Button variant="primary" iconAfter={ExternalLink} onclick={sendToGitHub}>
                Create the App on GitHub
              </Button>
            </div>

            <Field
              label="Code from GitHub"
              hint="If the tab did not return here, copy the code= value out of its address bar and paste it."
              error={errors.code ?? errors.state}
            >
              {#snippet children({ id, describedBy, invalid })}
                <Input bind:value={code} {id} {describedBy} {invalid} mono autocomplete="off" />
              {/snippet}
            </Field>
          {:else}
            <p class="lede">
              {appSlug ? `${appSlug} exists` : 'The App exists'} and its key is sealed here. It cannot
              do anything yet: an App has to be installed on the account before it can see any repositories.
            </p>

            {#if installUrl}
              <div>
                <a class="install" href={installUrl} target="_blank" rel="noopener noreferrer">
                  Install it on {target || 'the account'}
                  <ExternalLink size={14} aria-hidden="true" />
                </a>
              </div>
            {/if}

            <Field
              label="Installation ID"
              hint="After installing, GitHub's address bar ends in /installations/12345678. That number is this."
              error={errors.installation_id}
            >
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={installationId}
                  {id}
                  {describedBy}
                  {invalid}
                  type="number"
                  mono
                />
              {/snippet}
            </Field>
          {/if}

          {#if failure}
            <p class="failure" role="alert">{failure}</p>
          {/if}
        </div>
      {:else}
        <div class="stack">
          <p class="lede">
            Use this when the App already exists. The private key is sealed with this instance's
            encryption key before it is stored, and is never shown again.
          </p>

          <div class="pair">
            <Field label="App ID" error={errors.app_id}>
              {#snippet children({ id, describedBy, invalid })}
                <Input bind:value={manualAppId} {id} {describedBy} {invalid} type="number" mono />
              {/snippet}
            </Field>
            <Field label="Installation ID" error={errors.installation_id}>
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={manualInstallationId}
                  {id}
                  {describedBy}
                  {invalid}
                  type="number"
                  mono
                />
              {/snippet}
            </Field>
          </div>

          <Field label="Organisation or repository" error={errors.target}>
            {#snippet children({ id, describedBy, invalid })}
              <Input bind:value={manualTarget} {id} {describedBy} {invalid} mono />
            {/snippet}
          </Field>

          <RadioGroup
            bind:value={manualTargetType}
            name="manual-target-type"
            legend="Target type"
            inline
            options={[
              { value: 'org', label: 'Organisation' },
              { value: 'repo', label: 'A single repository' },
            ]}
          />

          <Field
            label="API base URL"
            hint="Leave empty for github.com."
            error={errors.api_base_url}
          >
            {#snippet children({ id, describedBy, invalid })}
              <Input
                bind:value={manualApiBase}
                {id}
                {describedBy}
                {invalid}
                type="url"
                mono
                placeholder="https://api.github.com"
              />
            {/snippet}
          </Field>

          <Field
            label="Private key"
            hint="The PEM GitHub gave you when you generated it. It starts with -----BEGIN."
            error={errors.private_key}
          >
            {#snippet children({ id, describedBy, invalid })}
              <Textarea
                bind:value={manualKey}
                {id}
                {describedBy}
                {invalid}
                rows={5}
                mono
                placeholder="-----BEGIN RSA PRIVATE KEY-----"
              />
            {/snippet}
          </Field>

          <Field
            label="Webhook secret"
            hint="Optional, but without it this controller cannot verify that a delivery really came from GitHub."
            error={errors.webhook_secret}
          >
            {#snippet children({ id, describedBy, invalid })}
              <Input bind:value={manualSecret} {id} {describedBy} {invalid} type="password" />
            {/snippet}
          </Field>

          {#if failure}
            <p class="failure" role="alert">{failure}</p>
          {/if}
        </div>
      {/if}
    {/snippet}
  </Tabs>

  {#snippet footer()}
    {#if tab === 'manifest'}
      <Button variant="ghost" disabled={busy} onclick={() => (step === 0 ? close() : (step -= 1))}>
        {step === 0 ? 'Cancel' : 'Back'}
      </Button>
      {#if step === 0}
        <Button
          variant="primary"
          loading={busy}
          disabled={!target.trim() || Boolean(targetError)}
          onclick={buildManifest}
        >
          Build the manifest
        </Button>
      {:else if step === 1}
        <Button variant="primary" loading={busy} disabled={!code.trim()} onclick={exchange}>
          Exchange the code
        </Button>
      {:else}
        <Button variant="primary" loading={busy} disabled={!installationId.trim()} onclick={record}>
          Finish
        </Button>
      {/if}
    {:else}
      <Button variant="ghost" disabled={busy} onclick={close}>Cancel</Button>
      <Button
        variant="primary"
        loading={busy}
        disabled={!manualAppId.trim() || !manualInstallationId.trim() || !manualTarget.trim()}
        onclick={connectExisting}
      >
        Connect
      </Button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .steps {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-4);
    margin: 0 0 var(--z-space-4);
    padding: 0;
    list-style: none;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .steps li {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  .steps li.active {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }
  .steps li.done {
    color: var(--z-text-muted);
  }
  .marker {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-5);
    height: var(--z-space-5);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-full);
    font-size: var(--z-text-2xs);
  }
  .steps li.active .marker {
    border-color: var(--z-accent);
    background: var(--z-accent-subtle);
    color: var(--z-accent);
  }
  .steps li.done .marker {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
    color: var(--z-idle);
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  .lede {
    margin: 0;
    max-width: 72ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .pair {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: var(--z-space-4);
  }
  .install {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    height: var(--z-space-8);
    padding: 0 var(--z-space-4);
    border-radius: var(--z-radius-md);
    background: var(--z-accent);
    color: var(--z-accent-contrast);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    text-decoration: none;
  }
  .install:hover {
    background: var(--z-accent-hover);
  }
  .failure {
    margin: 0;
    padding: var(--z-space-3);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-danger-subtle);
    color: var(--z-text);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
</style>
