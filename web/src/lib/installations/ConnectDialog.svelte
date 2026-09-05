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
  import { storage } from '$lib/state/prefs.svelte';
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
    /** The state that went out with the manifest, echoed back on the same redirect. */
    initialState?: string;
    /** The installation ID GitHub returns with once the App has been installed. */
    initialInstallationId?: string;
    oncreated?: () => void;
    /**
     * The code has been spent. The page that owns the address bar takes the
     * code and state out of it here, so a reload of this tab does not try to
     * exchange a code GitHub has already honoured.
     */
    onexchanged?: () => void;
    onclose?: () => void;
  }

  let {
    open = $bindable(false),
    initialCode = '',
    initialState = '',
    initialInstallationId = '',
    oncreated,
    onexchanged,
    onclose,
  }: Props = $props();

  /**
   * The flow crosses tabs twice: GitHub is asked to create the App in a new tab
   * and returns the operator there, and the install link opens another one
   * again. Neither knows what was typed on the first step, so what is needed to
   * finish is kept where any tab on this origin can read it.
   *
   * Nothing here is a secret. The App's private key never reaches the browser:
   * it stays sealed on the controller, which hands it out to nothing. This is
   * the target, the App's public identifiers, and the handshake state that is
   * already in the address bar GitHub redirected to.
   */
  /**
   * The square mark, served from the controller so that an operator on an
   * air-gapped network has it to hand rather than being sent to a website.
   */
  const APP_LOGO = '/brand/app-logo.png';

  const PROGRESS_KEY = 'zoomies.github.connect';
  /** Matches the controller's manifest TTL: after that the handshake is dead anyway. */
  const PROGRESS_TTL = 60 * 60 * 1000;

  interface Progress {
    savedAt: number;
    target: string;
    targetType: string;
    apiBase: string;
    manifestState: string;
    appId: number | null;
    appSlug: string;
    installUrl: string;
    settingsUrl: string;
    step: number;
  }

  function loadProgress(): Progress | null {
    const raw = storage.get(PROGRESS_KEY);
    if (!raw) return null;
    try {
      const parsed = JSON.parse(raw) as Partial<Progress>;
      if (typeof parsed?.savedAt !== 'number' || Date.now() - parsed.savedAt > PROGRESS_TTL) {
        storage.remove(PROGRESS_KEY);
        return null;
      }
      return parsed as Progress;
    } catch {
      storage.remove(PROGRESS_KEY);
      return null;
    }
  }

  function saveProgress(): void {
    storage.set(
      PROGRESS_KEY,
      JSON.stringify({
        savedAt: Date.now(),
        target,
        targetType,
        apiBase,
        manifestState,
        appId,
        appSlug,
        installUrl,
        settingsUrl,
        step,
      } satisfies Progress),
    );
  }

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
  /**
   * The App ID typed on the last step, for the tab that GitHub returned the
   * installation to when it is not the tab that created the App: nothing in
   * that browser knows which App the installation belongs to, and the App ID
   * is the one thing the controller needs to match it to the key it holds.
   */
  let appIdInput = $state('');
  /** The code came in on the address bar: GitHub has created the App already. */
  let arrivedWithCode = $state(false);

  /* -- what the server hands back -------------------------------------------- */

  let postUrl = $state('');
  let manifest = $state('');
  let manifestState = $state('');
  let appId = $state<number | null>(null);
  let appSlug = $state('');
  let installUrl = $state('');
  let settingsUrl = $state('');

  /* -- the manual path -------------------------------------------------------- */

  let manualAppId = $state('');
  let manualInstallationId = $state('');
  let manualTarget = $state('');
  let manualTargetType = $state('org');
  let manualApiBase = $state('');
  let manualKey = $state('');
  let manualSecret = $state('');

  /**
   * Pick up where the other tab left off.
   *
   * Which step that is comes from what GitHub sent back: a code means the App
   * has just been created and needs exchanging, an installation ID means it has
   * been installed and only needs recording.
   */
  let restored = false;
  $effect(() => {
    if (!open || restored) return;
    restored = true;

    const saved = loadProgress();
    if (saved) {
      if (!target) target = saved.target ?? '';
      if (saved.targetType) targetType = saved.targetType;
      if (!apiBase) apiBase = saved.apiBase ?? '';
      if (!manifestState) manifestState = saved.manifestState ?? '';
      if (appId === null) appId = saved.appId ?? null;
      if (!appSlug) appSlug = saved.appSlug ?? '';
      if (!installUrl) installUrl = saved.installUrl ?? '';
      if (!settingsUrl) settingsUrl = saved.settingsUrl ?? '';
      if (saved.step > step) step = saved.step;
    }
    // The state in the address bar is the one GitHub just echoed, so it wins
    // over anything left behind by an earlier attempt.
    if (initialState) manifestState = initialState;
    if (initialCode && !code) {
      // A handshake this browser already carried past the exchange is not
      // undone by the code still sitting in the address bar -- a reload of
      // the callback tab, say. That code is spent; step three is where the
      // flow left off.
      const exchanged = saved?.appId != null && (saved.step ?? 0) >= 2;
      if (!exchanged) {
        code = initialCode;
        step = 1;
        arrivedWithCode = true;
      }
    }
    if (initialInstallationId && !installationId) {
      installationId = initialInstallationId;
      // GitHub only ever sends this after an installation succeeded, so it is
      // never noise. When this browser does not know which App was created,
      // the last step asks for the App ID rather than starting over.
      step = 2;
    }
    // GitHub has done its part and the state is in the address bar: there is
    // nothing for the operator to decide before the exchange, so it runs.
    if (arrivedWithCode && manifestState) void exchange();
  });

  function reset(): void {
    storage.remove(PROGRESS_KEY);
    restored = false;
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
    appIdInput = '';
    arrivedWithCode = false;
    postUrl = '';
    manifest = '';
    manifestState = '';
    appId = null;
    appSlug = '';
    installUrl = '';
    settingsUrl = '';
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

  /**
   * The fields each step of the manifest tab renders. A field error for
   * anything else -- the private key, on the last step, when the controller
   * no longer holds it -- would otherwise be attached to an input that is not
   * on screen, leaving a failure line that names no cause.
   */
  const STEP_FIELDS: readonly (readonly string[])[] = [
    ['target', 'target_type', 'name', 'api_base_url'],
    ['code', 'state'],
    ['installation_id', 'app_id'],
  ];

  function report(cause: unknown, fallback: string): void {
    if (cause instanceof ApiError) {
      errors = cause.fieldErrors();
      failure = cause.message;
      if (tab === 'manifest') {
        const shown = STEP_FIELDS[step] ?? [];
        const hidden = Object.entries(errors)
          .filter(([field]) => !shown.includes(field))
          .map(([, message]) => message);
        if (hidden.length > 0) failure = `${failure} ${hidden.join(' ')}`;
      }
    } else {
      failure = fallback;
    }
  }

  /* -- step one: build the manifest ------------------------------------------- */

  /**
   * What GitHub will and will not let a private App do. A repository target
   * creates the App on the operator's own account, and GitHub installs a
   * private App only on the account that owns it -- so for a repository that
   * belongs to an organisation, the App has to be the organisation's, scoped
   * to that one repository at install time. Said here, before the App exists,
   * rather than discovered on GitHub's install page.
   */
  const targetHint = $derived(
    targetType === 'repo'
      ? 'A repository App is created on your own account, and GitHub only lets a private App be installed there. For a repository owned by an organisation, choose Organisation and pick just that repository when you install.'
      : 'The account whose runners this App will manage.',
  );

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
      builtFrom = JSON.stringify([target.trim(), targetType, apiBase.trim(), appName.trim()]);
      step = 1;
      saveProgress();
    } catch (cause) {
      report(cause, 'The manifest could not be built.');
    } finally {
      busy = false;
    }
  }

  /**
   * Going back and editing the description invalidates the manifest that was
   * built from the old answers.
   *
   * GitHub reads the manifest body, not the form on screen, so a stale one
   * creates an App for the wrong target -- or is rejected outright, which the
   * operator experiences as "it did not take my form". Clearing it here makes
   * the first step build a new one before the second step can post anything.
   */
  let builtFrom = $state('');
  $effect(() => {
    const now = JSON.stringify([target.trim(), targetType, apiBase.trim(), appName.trim()]);
    if (!postUrl) {
      builtFrom = now;
      return;
    }
    if (builtFrom !== '' && builtFrom !== now) {
      postUrl = '';
      manifest = '';
      manifestState = '';
      if (step > 0 && !code.trim()) step = 0;
    }
  });

  /**
   * Where the manifest is posted: the endpoint the API returned, with the
   * handshake state appended, because the API returns the two separately.
   */
  const manifestAction = $derived.by(() => {
    if (!postUrl) return '';
    try {
      const url = new URL(postUrl);
      if (manifestState) url.searchParams.set('state', manifestState);
      return url.toString();
    } catch {
      return postUrl;
    }
  });

  /**
   * The manifest goes to GitHub as a real form in the markup, submitted by a
   * real submit button.
   *
   * Two things have to hold for the POST to arrive. The form must be a real
   * one: a form built in script, submitted with `form.submit()` and torn down
   * in the same turn, is not reliably treated as user-initiated and can leave
   * the new tab on a blank page. And the page's Content-Security-Policy must
   * name GitHub in `form-action`, which the controller does (see
   * contentSecurityPolicy in internal/api/router.go). That second one was the
   * real cause of the "you have to reload the GitHub tab, and then the form is
   * empty" report: a policy of 'self' alone makes the browser refuse the
   * submission without a word on screen, and reloading turns the POST into a
   * GET, which GitHub answers with its blank create-an-App form.
   */

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
      settingsUrl = result.settings_url ?? '';
      // In the tab GitHub redirected to, the form on the first step was never
      // filled in. The controller remembers what the manifest asked for, and
      // returns it here so the last step has a target to record.
      if (!target) target = result.target ?? '';
      if (result.target_type) targetType = result.target_type;
      step = 2;
      saveProgress();
      onexchanged?.();
    } catch (cause) {
      report(cause, 'That code could not be exchanged.');
    } finally {
      busy = false;
    }
  }

  /* -- step three: record the installation ------------------------------------- */

  async function record(): Promise<void> {
    const app = appId ?? Number(appIdInput.trim());
    const id = Number(installationId.trim());
    if (!Number.isInteger(app) || app <= 0 || !Number.isInteger(id) || id <= 0) return;
    busy = true;
    errors = {};
    failure = '';
    try {
      await createInstallation({
        app_id: app,
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
              hint={targetHint}
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
            {#if arrivedWithCode}
              <p class="lede">
                GitHub has created the App and sent this tab back with a code. The code is exchanged
                here for the App's credentials, which stay sealed on this controller; if the
                exchange did not go through, the button below tries it again.
              </p>
            {:else if manifest}
              <p class="lede">
                The next button opens GitHub in a new tab with the manifest already filled in.
                Confirm it there; GitHub creates the App and sends the browser back here with a code
                in the address bar.
              </p>

              <form method="POST" action={manifestAction} target="_blank" rel="noopener">
                <input type="hidden" name="manifest" value={manifest} />
                <Button type="submit" variant="primary" iconAfter={ExternalLink}>
                  Create the App on GitHub
                </Button>
              </form>
            {:else}
              <p class="lede">
                The manifest was built in another tab, so this one has nothing to send to GitHub.
                Paste the code GitHub gave that tab, or go back a step and build the manifest here.
              </p>
            {/if}

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
            {#if appId !== null}
              <p class="lede">
                {appSlug ? `${appSlug} exists` : 'The App exists'} and its key is sealed here. It cannot
                do anything yet: an App has to be installed on the account before it can see any repositories.
              </p>
            {:else}
              <p class="lede">
                GitHub reports that installation {installationId || 'of the App'} was created, but this
                browser does not know which App it belongs to -- that was in the tab the flow started
                from. The App ID is on the App's settings page, next to its name; this controller still
                holds the key it created, for an hour.
              </p>

              <Field label="App ID" error={errors.app_id}>
                {#snippet children({ id, describedBy, invalid })}
                  <Input bind:value={appIdInput} {id} {describedBy} {invalid} type="number" mono />
                {/snippet}
              </Field>
            {/if}

            {#if installUrl}
              <div>
                <a class="install" href={installUrl} target="_blank" rel="noopener noreferrer">
                  Install it on {target || 'the account'}
                  <ExternalLink size={14} aria-hidden="true" />
                </a>
              </div>
            {/if}

            <div class="logo-step">
              <img class="logo-preview" src={APP_LOGO} alt="" width="56" height="56" />
              <div class="logo-copy">
                <p class="logo-title">Give it the Zoomies mark</p>
                <p>
                  An App manifest cannot carry a logo — GitHub only takes an upload — so the App is
                  wearing the grey default, and it signs every "Set up job" line in the
                  organisation's logs. Download the mark and upload it under
                  <em>Display information</em>.
                </p>
                <p class="logo-actions">
                  <a href={APP_LOGO} download="zoomies-app-logo.png">Download the mark</a>
                  {#if settingsUrl}
                    <a href={settingsUrl} target="_blank" rel="noopener noreferrer">
                      Open the App's settings
                      <ExternalLink size={12} aria-hidden="true" />
                    </a>
                  {/if}
                </p>
              </div>
            </div>

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
        <Button
          variant="primary"
          loading={busy}
          disabled={!installationId.trim() || (appId === null && !appIdInput.trim())}
          onclick={record}
        >
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
  .logo-step {
    display: flex;
    gap: var(--z-space-3);
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .logo-preview {
    flex: none;
    border-radius: var(--z-radius-md);
  }
  .logo-copy {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .logo-copy p {
    margin: 0;
  }
  .logo-title {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }
  .logo-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    margin-top: var(--z-space-1);
  }
  .logo-actions a {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: var(--z-accent);
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
