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
  import { CircleAlert, ExternalLink, RotateCcw } from '@lucide/svelte';
  import {
    ApiError,
    createAppManifest,
    createInstallation,
    exchangeAppManifest,
    verifyInstallation,
  } from '$lib/api/client';
  import type { InstallationHealth, TargetType } from '$lib/api/types';
  import { session } from '$lib/state/session.svelte';
  import { storage } from '$lib/state/prefs.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
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

  /**
   * Progress is stored per target, so an operator connecting a second
   * organisation in another tab does not overwrite the first one's handshake.
   */
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
  /** Set when this dialog opened onto a handshake somebody left behind. */
  let resumed = $state<{ target: string; savedAt: number } | null>(null);
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
      // Landing on step three of somebody's abandoned attempt, with an install
      // link pointing at a different App and no explanation, is how an
      // operator with two organisations experienced every second run. The
      // banner names what is being resumed and offers the way out.
      if (saved.step > 0 && !initialCode && !initialInstallationId) {
        resumed = { target: saved.target ?? '', savedAt: saved.savedAt };
      }
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
    resumed = null;
    // An operator who used the manual tab once got it again on their next
    // "Connect GitHub", skipping straight past the path that is recommended.
    tab = 'manifest';
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

  /** Throw away a stored handshake and begin again, without closing. */
  function startOver(): void {
    reset();
    restored = true;
  }

  /**
   * A failure from one tab has nothing to say about the other, and leaving it
   * on screen put "the App was created, but this controller no longer has the
   * setup state" over a form it has no bearing on.
   */
  let lastTab = 'manifest';
  $effect(() => {
    if (tab === lastTab) return;
    lastTab = tab;
    failure = '';
    errors = {};
  });

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
  const MANUAL_FIELDS: readonly string[] = [
    'app_id',
    'installation_id',
    'target',
    'target_type',
    'api_base_url',
    'private_key',
    'webhook_secret',
  ];

  /**
   * The messages a failure has to show, and where.
   *
   * A field error whose input is on screen is rendered under that input. One
   * whose input is not -- the private key, on a step that does not ask for it
   * -- would otherwise be a failure line naming no cause, so it is listed under
   * the summary instead. They are kept as a list rather than joined into the
   * summary with a space: two independent sentences glued together read as one
   * broken one ("...could not be connected this controller no longer holds the
   * credentials..."), and the seam is where an operator stops trusting the
   * message.
   */
  let extraFailures = $state<string[]>([]);
  /** Bumped on every report, so an identical repeat is announced again. */
  let failureSeq = $state(0);
  let failureBox = $state<HTMLDivElement | null>(null);

  /**
   * A step change is announced the way App.svelte announces a route change.
   *
   * This matters most on the path that matters most: arriving back from GitHub
   * runs the exchange on its own, so the dialog rewrites itself from step two
   * to step three with focus wherever the browser left it and nothing said.
   */
  let announcement = $state('');
  let panel = $state<HTMLFormElement | null>(null);
  let lastStep = -1;
  $effect(() => {
    const n = step;
    if (n === lastStep) return;
    const first = lastStep === -1;
    lastStep = n;
    const title = STEPS[n]?.title ?? '';
    announcement = `Step ${n + 1} of ${STEPS.length}, ${title}.`;
    // Focus follows the panel, the way the pool wizard's does. Not on the very
    // first render, where trapFocus is already placing the cursor.
    if (!first) panel?.focus();
  });

  /** What the footer's primary does on this step. */
  function advance(): void {
    if (busy) return;
    if (step === 0) void buildManifest();
    else if (step === 1) void exchange();
    else void record();
  }

  /**
   * Bring the failure into view and take focus.
   *
   * `role="alert"` announces it, but on a dialog whose body scrolls under a
   * pinned footer the message can be off screen for a sighted operator whose
   * eye is on the button they just pressed.
   */
  $effect(() => {
    if (!failureBox) return;
    failureBox.scrollIntoView({ block: 'nearest' });
    failureBox.focus();
  });

  function report(cause: unknown, fallback: string): void {
    failureSeq += 1;
    if (cause instanceof ApiError) {
      errors = cause.fieldErrors();
      failure = cause.message;
      const shown = tab === 'manifest' ? (STEP_FIELDS[step] ?? []) : MANUAL_FIELDS;
      extraFailures = Object.entries(errors)
        .filter(([field]) => !shown.includes(field))
        .map(([, message]) => message);
      // The exchange handler puts the same sentence in the envelope and in the
      // `code` field, so the operator read the identical paragraph twice.
      if (Object.values(errors).some((message) => message === failure)) failure = '';
    } else {
      failure = fallback;
      extraFailures = [];
    }
  }

  function clearFailure(): void {
    failure = '';
    extraFailures = [];
    errors = {};
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

  /**
   * What GitHub is about to be told, before the operator is sent there.
   *
   * The terminal installer prints both of these -- the webhook URL and the
   * exact permission set -- before opening a browser, and they are the two
   * things somebody about to install an App on their organisation actually
   * wants to read. The promise "exactly the permissions it needs, and nothing
   * more" is worth more when it is followed by the list.
   */
  const webhookURL = $derived(session.meta?.webhook_url ?? '');
  const permissions = $derived([
    targetType === 'repo'
      ? "administration: write -- register and remove this repository's runners"
      : "organization_self_hosted_runners: write -- register and remove the org's runners",
    'actions: read -- read workflow runs and jobs for the fallback poller',
    'metadata: read -- required by GitHub for every App',
    'workflow_job events -- the webhook that makes scaling instant',
  ]);

  /**
   * A GitHub App's webhook URL is fixed when GitHub creates it, and it is
   * built from server.external_url. Without one the manifest cannot be built
   * at all -- the API says so, but only after the whole form has been filled
   * in, and it attaches the finding to the "Organisation or repository" field,
   * so a sentence about a configuration key appears under an input it has
   * nothing to do with. The client already has the answer, so it says so first.
   */
  const externalURL = $derived(session.meta?.external_url ?? '');
  const notReachable = $derived(externalURL === '');

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
    clearFailure();
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

  /* -- step two: exchange the code -------------------------------------------- */

  async function exchange(): Promise<void> {
    if (!code.trim()) return;
    busy = true;
    clearFailure();
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

  /**
   * The number out of whatever the operator had to hand.
   *
   * GitHub returns here after an install, so the address bar they are looking
   * at is a Zoomies URL with `installation_id=` in it -- not the GitHub one the
   * old hint described. The obvious recovery, pasting that whole URL, used to
   * fail in silence: a `type="number"` input reports an empty string for
   * anything it cannot parse, so nothing appeared and no error was shown. The
   * terminal installer has accepted a pasted URL all along; this is the same
   * rule.
   */
  function parseId(raw: string): string {
    const text = raw.trim();
    if (/^\d+$/.test(text)) return text;
    const fromQuery = /[?&]installation_id=(\d+)/.exec(text);
    if (fromQuery?.[1]) return fromQuery[1];
    const fromPath = /\/installations\/(\d+)/.exec(text);
    if (fromPath?.[1]) return fromPath[1];
    return text.replace(/\D+/g, '');
  }

  const installationIdValue = $derived(parseId(installationId));
  const appIdValue = $derived(parseId(appIdInput));

  async function record(): Promise<void> {
    const app = appId ?? Number(appIdValue);
    const id = Number(installationIdValue);
    if (!Number.isInteger(app) || app <= 0 || !Number.isInteger(id) || id <= 0) return;
    busy = true;
    clearFailure();
    try {
      const created = await createInstallation({
        app_id: app,
        installation_id: id,
        target: target.trim(),
        target_type: targetType as TargetType,
        // Empty means "whatever this controller is configured to talk to".
        api_base_url: apiBase.trim(),
        // The private key is already held, sealed, from the exchange.
        private_key: '',
      });
      // "Zoomies can now create runners for this target" was a claim, not a
      // fact: an App whose organisation permission was unticked at install
      // time, or an org App installed on a personal account, records perfectly
      // and fails much later in a way that reads like a Zoomies bug. The
      // terminal installer probes the credentials before claiming success;
      // this does the same, and stays open when the probe is unhappy.
      await settle(created.id ?? '', target.trim());
    } catch (cause) {
      report(cause, 'The installation could not be recorded.');
    } finally {
      busy = false;
    }
  }

  /** The verified result of a connection, shown in place of the form. */
  let done = $state<{ target: string; health: InstallationHealth | null } | null>(null);

  async function settle(id: string, targetName: string): Promise<void> {
    oncreated?.();
    let health: InstallationHealth | null = null;
    try {
      if (id) health = await verifyInstallation(id);
    } catch {
      // The connection is recorded either way. A probe that could not be made
      // is not a reason to withhold the good news, only to stop promising it.
      health = null;
    }
    done = { target: targetName, health };
  }

  /* -- the manual path --------------------------------------------------------- */

  /**
   * The same shape check the manifest tab makes. Without it an `acme/widgets`
   * typed against Organisation is stored happily, and the operator's first
   * symptom is GitHub 404ing a runner registration -- a failure that looks
   * nothing like the setup mistake it is.
   */
  const manualTargetError = $derived(
    manualTarget.trim() === ''
      ? ''
      : manualTargetType === 'repo' && !manualTarget.includes('/')
        ? 'A repository target is written owner/repo.'
        : manualTargetType === 'org' && manualTarget.includes('/')
          ? 'An organisation target is just its name, with no slash.'
          : '',
  );

  /** The server rejects an empty private key, so the form says so first. */
  const manualKeyError = $derived(
    manualKey.trim() !== '' && !manualKey.trim().startsWith('-----BEGIN')
      ? 'That does not look like a PEM. It starts with -----BEGIN.'
      : '',
  );

  const manualIncomplete = $derived(
    !manualAppId.trim() ||
      !manualInstallationId.trim() ||
      !manualTarget.trim() ||
      !manualKey.trim() ||
      Boolean(manualTargetError) ||
      Boolean(manualKeyError),
  );

  async function connectExisting(): Promise<void> {
    busy = true;
    clearFailure();
    try {
      const created = await createInstallation({
        app_id: Number(parseId(manualAppId)),
        installation_id: Number(parseId(manualInstallationId)),
        target: manualTarget.trim(),
        target_type: manualTargetType as TargetType,
        api_base_url: manualApiBase.trim(),
        private_key: manualKey,
        webhook_secret: manualSecret || undefined,
      });
      await settle(created.id ?? '', manualTarget.trim());
    } catch (cause) {
      report(cause, 'That App could not be connected.');
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Connect GitHub" size="lg" onclose={close}>
  <output class="sr-only" aria-live="polite">{announcement}</output>
  <Tabs
    bind:value={tab}
    label="How to connect"
    tabs={[
      { id: 'manifest', label: 'Create a new App' },
      { id: 'existing', label: 'Use an App you already have' },
    ]}
  >
    {#snippet children(active)}
      {#if done}
        <!-- The flow does not end at a recorded row: it ends when the operator
             knows the credentials work and what to do next. -->
        <div class="stack">
          <div class="settled" class:bad={done.health?.ok === false}>
            <p class="settled-title">
              {done.health?.ok === false
                ? `${done.target} is connected, but something is missing`
                : `${done.target} is connected`}
            </p>
            {#if done.health?.ok === false}
              <p>Zoomies reached GitHub with these credentials, and found:</p>
              <ul>
                {#each done.health.missing_permissions ?? [] as name (name)}
                  <li>The App is missing the <code>{name}</code> permission.</li>
                {/each}
                {#each done.health.missing_events ?? [] as name (name)}
                  <li>The App is not subscribed to the <code>{name}</code> event.</li>
                {/each}
                {#if done.health.message}<li>{done.health.message}</li>{/if}
              </ul>
              <p>Fix them on the App's settings page on GitHub, then check it again here.</p>
            {:else if done.health}
              <p>
                Zoomies signed in as the App and confirmed it can register runners for this target.
              </p>
            {:else}
              <p>
                The connection is recorded. Use <em>Check</em> on its card to confirm the credentials
                work.
              </p>
            {/if}
          </div>
        </div>
      {:else if active === 'manifest'}
        {#if resumed}
          <div class="resume">
            <RotateCcw size={15} aria-hidden="true" />
            <div>
              <p>
                Picking up where you left off connecting <strong
                  >{resumed.target || 'GitHub'}</strong
                >.
              </p>
              <Button variant="ghost" size="sm" onclick={startOver}>Start a new connection</Button>
            </div>
          </div>
        {/if}

        <!-- aria-current sits on the item, not on an inner span, and each item
             carries its own position: a screen reader used to hear a
             three-item list with no sense of where in it the operator was. -->
        <ol class="steps" aria-label="Connect GitHub">
          {#each STEPS as s, index (s.id)}
            <li
              class:done={index < step}
              class:active={index === step}
              aria-current={index === step ? 'step' : undefined}
            >
              <span class="marker" aria-hidden="true">{index + 1}</span>
              <span><span class="sr-only">Step {index + 1} of {STEPS.length}: </span>{s.title}</span
              >
            </li>
          {/each}
        </ol>

        <!--
          A real form, so Enter submits.

          Every other form in the product does -- Bootstrap, Login, the pool
          wizard -- so this was the one place in the first run where the habit
          failed, and it failed on the last keystroke of the whole connection.
          The footer's primary belongs to it through `form=`, which lets the
          button stay pinned in the footer.
        -->
        <form
          id="connect-step"
          class="stack"
          bind:this={panel}
          tabindex="-1"
          role="group"
          aria-label={STEPS[step]?.title}
          onsubmit={(event) => {
            event.preventDefault();
            advance();
          }}
        >
          <!-- At the top, where the eye returns after a failed action: at the
               bottom it sat below the fold of the dialog's scrolling body,
               under a pinned footer, so a 422 looked like nothing happened. -->
          {#if failure || extraFailures.length > 0}
            {#key failureSeq}
              <div class="failure" role="alert" tabindex="-1" bind:this={failureBox}>
                {#if failure}<p>{failure}</p>{/if}
                {#if extraFailures.length > 0}
                  <ul>
                    {#each extraFailures as message (message)}<li>{message}</li>{/each}
                  </ul>
                {/if}
              </div>
            {/key}
          {/if}
          {#if step === 0}
            {#if notReachable}
              <!-- A GitHub App's webhook URL is fixed at creation and is built
                   from server.external_url. Without one, filling this form in
                   ends in a 422 attached to the wrong field. Say it first. -->
              <div class="blocked" role="status">
                <CircleAlert size={16} aria-hidden="true" />
                <div>
                  <p class="blocked-title">This controller has no external URL yet</p>
                  <p>
                    GitHub is told where to deliver webhooks when the App is created, and that
                    address cannot be changed from here afterwards. Set
                    <code>server.external_url</code> to the address GitHub can reach, restart the controller,
                    and come back.
                  </p>
                </div>
              </div>
            {:else}
              <p class="lede">
                Zoomies builds a GitHub App manifest that asks for exactly the permissions it needs,
                and nothing more. Nothing is created until you confirm it on GitHub.
              </p>

              <div class="facts">
                <p class="facts-title">What GitHub will be told</p>
                <dl>
                  <dt>Webhook URL</dt>
                  <dd>
                    <code>{webhookURL}</code>
                    <CopyButton value={webhookURL} label="Copy the webhook URL" />
                  </dd>
                  <dt>Permissions</dt>
                  <dd>
                    <ul>
                      {#each permissions as line (line)}<li>{line}</li>{/each}
                    </ul>
                  </dd>
                </dl>
              </div>
            {/if}

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
                The next button takes you to GitHub with the manifest already filled in. Confirm it
                there and you will be brought straight back here, with the code in the address bar.
              </p>

              <!-- The button lives here; the form it submits is a sibling of
                   the step form, below, because a form cannot nest. -->
              <div>
                <Button
                  type="submit"
                  form="github-manifest"
                  variant="primary"
                  iconAfter={ExternalLink}
                >
                  Create the App on GitHub
                </Button>
              </div>
            {:else}
              <p class="lede">
                This browser has no manifest to send -- it was built somewhere else, or the page was
                reloaded. Go back a step to build one here, or paste the code GitHub gave you.
              </p>
            {/if}

            <!-- Folded away on the happy path: an empty "paste the code" field
                 shown before the operator has been to GitHub reads as the main
                 route rather than the fallback it is. -->
            <details class="fallback" open={arrivedWithCode || !manifest}>
              <summary>GitHub did not bring you back?</summary>
              <Field
                label="Code from GitHub"
                hint="Copy the code= value out of the address bar GitHub left you on, and paste it here."
                error={errors.code ?? errors.state}
              >
                {#snippet children({ id, describedBy, invalid })}
                  <Input bind:value={code} {id} {describedBy} {invalid} mono autocomplete="off" />
                {/snippet}
              </Field>
            </details>
          {:else}
            {#if appId !== null}
              <p class="lede">
                {appSlug ? `${appSlug} exists` : 'The App exists'} and its key is sealed here. It cannot
                do anything yet: an App has to be installed on the account before it can see any repositories.
              </p>
            {:else}
              <p class="lede">
                GitHub reports that installation {installationIdValue || 'of the App'} was created, but
                this browser does not know which App it belongs to. The App ID is on the App's settings
                page, next to its name; this controller still holds the key it created, for an hour.
              </p>

              <Field label="App ID" error={errors.app_id} required>
                {#snippet children({ id, describedBy, invalid })}
                  <Input
                    bind:value={appIdInput}
                    {id}
                    {describedBy}
                    {invalid}
                    inputmode="numeric"
                    mono
                  />
                {/snippet}
              </Field>
            {/if}

            {#if installUrl}
              <div>
                <Button variant="primary" href={installUrl} iconAfter={ExternalLink}>
                  Install it on {target || 'the account'}
                </Button>
              </div>
            {/if}

            <!--
              Directly after the install link, because it is the only thing
              still outstanding. GitHub sends the browser back here with the
              number in the address bar, so on the ordinary path this field
              fills itself; typing is the fallback, and pasting the whole URL
              works because parseId takes the number out of it.
            -->
            <Field
              label="Installation ID"
              hint="GitHub brings you back here with this in the address bar. If it did not, paste the URL it left you on and Zoomies will take the number out of it."
              error={errors.installation_id}
              required
            >
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={installationId}
                  {id}
                  {describedBy}
                  {invalid}
                  inputmode="numeric"
                  mono
                />
              {/snippet}
            </Field>

            <!-- Last, and folded away: worth doing, but it is a cosmetic
                 improvement to the App and it used to sit between the two
                 fields this step actually needs. -->
            <details class="logo-step">
              <summary>Give the App the Zoomies mark</summary>
              <div class="logo-body">
                <img class="logo-preview" src={APP_LOGO} alt="" width="56" height="56" />
                <div class="logo-copy">
                  <p>
                    An App manifest cannot carry a logo — GitHub only takes an upload — so the App
                    is wearing the grey default, and it signs every "Set up job" line in the
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
            </details>
          {/if}
        </form>

        <!--
          The manifest goes to GitHub as a real form in the markup, submitted by
          a real submit button.

          Two things have to hold for the POST to arrive. The form must be a
          real one: a form built in script, submitted with `form.submit()` and
          torn down in the same turn, is not reliably treated as user-initiated
          and can leave the new tab on a blank page. And the page's
          Content-Security-Policy must name GitHub in `form-action`, which the
          controller does (see contentSecurityPolicy in internal/api/router.go).
          That second one was the real cause of the "you have to reload the
          GitHub tab, and then the form is empty" report: a policy of 'self'
          alone makes the browser refuse the submission without a word on
          screen, and reloading turns the POST into a GET, which GitHub answers
          with its blank create-an-App form.

          It sits outside the step form because HTML has no nested forms, and
          the step form is what makes Enter work everywhere else in the dialog.
        -->
        {#if step === 1 && manifest}
          <form id="github-manifest" method="POST" action={manifestAction} hidden>
            <input type="hidden" name="manifest" value={manifest} />
          </form>
        {/if}
      {:else}
        <form
          id="connect-existing"
          class="stack"
          onsubmit={(event) => {
            event.preventDefault();
            if (!manualIncomplete && !busy) void connectExisting();
          }}
        >
          <!-- Same placement as the manifest tab: a failure belongs where the
               operator is looking, not under a form they have scrolled past. -->
          {#if failure || extraFailures.length > 0}
            {#key failureSeq}
              <div class="failure" role="alert" tabindex="-1" bind:this={failureBox}>
                {#if failure}<p>{failure}</p>{/if}
                {#if extraFailures.length > 0}
                  <ul>
                    {#each extraFailures as message (message)}<li>{message}</li>{/each}
                  </ul>
                {/if}
              </div>
            {/key}
          {/if}

          <p class="lede">
            Use this when the App already exists. The private key is sealed with this instance's
            encryption key before it is stored, and is never shown again.
          </p>

          <div class="pair">
            <Field label="App ID" error={errors.app_id} required>
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={manualAppId}
                  {id}
                  {describedBy}
                  {invalid}
                  inputmode="numeric"
                  mono
                />
              {/snippet}
            </Field>
            <Field label="Installation ID" error={errors.installation_id} required>
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={manualInstallationId}
                  {id}
                  {describedBy}
                  {invalid}
                  inputmode="numeric"
                  mono
                />
              {/snippet}
            </Field>
          </div>

          <Field
            label="Organisation or repository"
            error={errors.target ?? manualTargetError}
            required
          >
            {#snippet children({ id, describedBy, invalid })}
              <Input
                bind:value={manualTarget}
                {id}
                {describedBy}
                invalid={invalid || Boolean(manualTargetError)}
                placeholder={manualTargetType === 'repo' ? 'acme/widgets' : 'acme'}
                mono
              />
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
            error={errors.private_key ?? manualKeyError}
            required
          >
            {#snippet children({ id, describedBy, invalid })}
              <Textarea
                bind:value={manualKey}
                {id}
                {describedBy}
                invalid={invalid || Boolean(manualKeyError)}
                rows={5}
                mono
                placeholder="-----BEGIN RSA PRIVATE KEY-----"
              />
            {/snippet}
          </Field>

          <Field
            label="Webhook secret"
            hint="Optional, but without it this controller cannot verify that a delivery really came from GitHub. Point the App's webhook at {webhookURL ||
              'this controller'}."
            error={errors.webhook_secret}
          >
            {#snippet children({ id, describedBy, invalid })}
              <Input bind:value={manualSecret} {id} {describedBy} {invalid} type="password" />
            {/snippet}
          </Field>
        </form>
      {/if}
    {/snippet}
  </Tabs>

  {#snippet footer()}
    {#if done}
      <Button variant="ghost" onclick={close}>Close</Button>
      <!-- The operator's first fifteen minutes do not end at a connected
           installation; they end at a job running on their own runner. -->
      <Button variant="primary" href="/pools/new" onclick={close}>Create a pool</Button>
    {:else if tab === 'manifest'}
      <Button variant="ghost" disabled={busy} onclick={() => (step === 0 ? close() : (step -= 1))}>
        {step === 0 ? 'Cancel' : 'Back'}
      </Button>
      {#if step === 0}
        <Button
          variant="primary"
          type="submit"
          form="connect-step"
          loading={busy}
          disabled={notReachable || !target.trim() || Boolean(targetError)}
        >
          Continue to GitHub
        </Button>
      {:else if step === 1}
        <Button
          variant="primary"
          type="submit"
          form="connect-step"
          loading={busy}
          disabled={!code.trim()}
        >
          Exchange the code
        </Button>
      {:else}
        <Button
          variant="primary"
          type="submit"
          form="connect-step"
          loading={busy}
          disabled={!installationIdValue || (appId === null && !appIdValue)}
        >
          Finish
        </Button>
      {/if}
    {:else}
      <Button variant="ghost" disabled={busy} onclick={close}>Cancel</Button>
      <Button
        variant="primary"
        type="submit"
        form="connect-existing"
        loading={busy}
        disabled={manualIncomplete}
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
  .logo-step {
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .logo-step summary {
    cursor: pointer;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .logo-body {
    display: flex;
    gap: var(--z-space-3);
    margin-top: var(--z-space-3);
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
  .failure p {
    margin: 0;
    text-wrap: pretty;
  }
  /* A list, not a paragraph: two independent sentences glued together with a
     space read as one broken one, and the seam is where trust goes. */
  .failure ul {
    margin: var(--z-space-2) 0 0;
    padding-inline-start: var(--z-space-5);
  }
  .failure li + li {
    margin-top: var(--z-space-1);
  }

  .resume,
  .blocked {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    margin-bottom: var(--z-space-4);
    padding: var(--z-space-3);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }
  .resume {
    border: 1px solid var(--z-accent-border);
    background: var(--z-accent-subtle);
    color: var(--z-text);
  }
  .resume :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--z-accent);
  }
  .resume div,
  .blocked div {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .resume p,
  .blocked p {
    margin: 0;
    text-wrap: pretty;
  }
  .blocked {
    margin-bottom: 0;
    border: 1px solid var(--z-pending-border);
    background: var(--z-pending-subtle);
    color: var(--z-text);
  }
  .blocked :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--z-pending);
  }
  .blocked-title {
    font-weight: var(--z-weight-medium);
  }

  /* The claim "exactly the permissions it needs" is worth more with the list
     under it, and this is the last screen before the operator leaves for
     GitHub. */
  .facts {
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .facts-title {
    margin: 0 0 var(--z-space-2);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .facts dl {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--z-space-1) var(--z-space-4);
    margin: 0;
  }
  .facts dt {
    color: var(--z-text-subtle);
  }
  .facts dd {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    min-width: 0;
  }
  .facts dd ul {
    margin: 0;
    padding-inline-start: var(--z-space-4);
  }
  .facts code,
  .settled code {
    font-family: var(--z-font-mono);
    word-break: break-all;
  }
  @media (max-width: 560px) {
    .facts dl {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .fallback summary {
    cursor: pointer;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .fallback[open] summary {
    margin-bottom: var(--z-space-3);
  }

  .settled {
    padding: var(--z-space-4);
    border: 1px solid var(--z-idle-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-idle-subtle);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .settled.bad {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
  }
  .settled p {
    margin: 0 0 var(--z-space-2);
    text-wrap: pretty;
  }
  .settled p:last-child {
    margin-bottom: 0;
  }
  .settled-title {
    font-weight: var(--z-weight-semibold);
  }
  .settled ul {
    margin: 0 0 var(--z-space-2);
    padding-inline-start: var(--z-space-5);
  }
</style>
