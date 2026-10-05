<!--
  Step three: what a runner is made of, how it is run, and how much Docker a job
  gets.

  The operating system is first because it is the answer with the widest blast
  radius: it picks the image every runner boots and it keeps them off hosts
  running something else. It is a list rather than a text field because it is
  served from the images Zoomies actually publishes, and a pool that names one
  we do not publish is a pool that validates and then never starts a runner.

  Architecture is not asked here. The Hosts step already asks, and the image
  reference does not change with it: every variant is published as a manifest
  covering both, so a host pulls its own.

  The two dangerous answers in the whole wizard also live here, so both are
  spelled out in the consequence rather than in a footnote, and the host-socket
  option cannot be left selected without a deliberate confirmation.
-->
<script lang="ts">
  import { ServerOff, ShieldAlert } from '@lucide/svelte';
  import type { BackendKind, DockerMode, Host, PoolPlatform } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import Button from '$lib/components/Button.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Select from '$lib/components/Select.svelte';
  import {
    BACKENDS,
    DOCKER_MODES,
    backendLabel,
    backendUnavailable,
    platformKey,
  } from './vocabulary';
  import type { BackendOffer } from './vocabulary';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    offers: readonly BackendOffer[];
    /** False until the fleet cache has landed, so we do not cry wolf about hosts. */
    hostsKnown: boolean;
    /** The operating systems a runner image is published for. */
    platforms: readonly PoolPlatform[];
    /** Every connected host, so the step can say how many match the platform. */
    hosts: readonly Host[];
    /** True when the pool is kept to some of the fleet, so the counts are of those. */
    restricted?: boolean;
    socketConfirmed?: boolean;
  }

  let {
    draft,
    errors,
    touch,
    offers,
    hostsKnown,
    platforms,
    hosts,
    restricted = false,
    socketConfirmed = $bindable(false),
  }: Props = $props();

  const uid = $props.id();
  const dockerModeErrorId = $derived(
    errors['docker_mode'] ? `${uid}-docker-mode-error` : undefined,
  );

  function offer(kind: BackendKind): BackendOffer | undefined {
    return offers.find((entry) => entry.kind === kind);
  }

  function availability(kind: BackendKind): string {
    if (!hostsKnown) return '';
    const found = offer(kind);
    if (!found) return '';
    // "matching" rather than "connected" once the pool is kept to some of the
    // fleet: the count is of the hosts it may actually land on.
    const noun = restricted ? 'matching host' : 'connected host';
    if (found.hosts === 0) {
      const none = `No ${noun} offers it`;
      return found.detail
        ? `${none}: ${found.detail}`
        : `${none}, so this pool would never place a runner.`;
    }
    return `Offered by ${pluralise(found.hosts, noun)}.`;
  }

  const backendOptions = $derived(
    BACKENDS.map((choice) => ({
      value: choice.value,
      label: choice.label,
      description: `${choice.consequence} ${availability(choice.value)}`.trim(),
    })),
  );

  const dindHosts = $derived(offer(draft.backend)?.dindHosts ?? 0);

  // Why this pool could not run as chosen, in the same sentence that stops the
  // wizard advancing, plus the backends it could move to.
  const unavailable = $derived(backendUnavailable(draft.backend, offers, hostsKnown, restricted));
  const runnable = $derived(
    offers.filter((entry) => entry.kind !== draft.backend && entry.hosts > 0),
  );

  const dockerOptions = $derived(
    DOCKER_MODES.map((choice) => {
      let description = choice.consequence;
      if (choice.value === 'dind' && hostsKnown && dindHosts === 0) {
        description += ' No connected host reports that it can do this.';
      }
      return { value: choice.value, label: choice.label, description };
    }),
  );

  const usesImage = $derived(draft.backend !== 'process');

  /* -- platform ----------------------------------------------------------- */

  const osOptions = $derived([
    { value: '', label: 'Any — use the default image' },
    ...platforms.map((p) => ({
      value: platformKey(p.os, p.os_version),
      label: p.default ? `${p.label} (default)` : (p.label ?? ''),
    })),
  ]);

  const chosenOS = $derived(platformKey(draft.platform_os, draft.platform_os_version));

  const chosen = $derived(
    platforms.find((p) => platformKey(p.os, p.os_version) === chosenOS) ?? undefined,
  );

  /**
   * The image this pool will actually boot. A pool that names its own image
   * keeps it; otherwise the platform picks one, and saying which turns an
   * abstract choice into a concrete one.
   */
  const effectiveImage = $derived(draft.image.trim() || chosen?.image || '');

  /**
   * How many connected hosts run this operating system. A pool that matches
   * none of them will never start a runner, and finding that out here is far
   * better than finding it out from an empty Runners page.
   *
   * Architecture is not counted here: the Hosts step asks for it, and the
   * image reference does not change with it -- every variant is published as a
   * manifest covering both, so the host pulls its own.
   */
  const matchingHosts = $derived(
    hosts.filter((host) => {
      if (draft.platform_os && host.platform?.os && host.platform.os !== draft.platform_os)
        return false;
      if (
        draft.platform_os_version &&
        host.platform?.os_version &&
        host.platform.os_version !== draft.platform_os_version
      )
        return false;
      return true;
    }).length,
  );

  function chooseOS(value: string): void {
    const found = platforms.find((p) => platformKey(p.os, p.os_version) === value);
    draft.platform_os = found?.os ?? '';
    draft.platform_os_version = found?.os_version ?? '';
    touch('platform.os');
  }

  // A pool's docker_mode gives its jobs a daemon; the image has to bring the
  // client. The stock runner image carries none on purpose, so a pool that
  // asks for a daemon while on a tag this build publishes is switched to the
  // stock image's Docker variant -- the same image plus a client, under the
  // same tag -- as it is saved. Say so on the step that decides it, so the
  // image the pool's page shows afterwards is not a surprise; the review step
  // shows the image the server answers with, which covers an empty field as
  // well.
  //
  // What is not switched is said too, each for its own reason. A digest names
  // one exact image. A tag from some other build -- a commit pin, a release
  // this build knows nothing about -- may name a run whose variant was never
  // published, and moving a pool onto a tag the registry lacks would stop it
  // running every job rather than only the Docker ones. An image that merely
  // looks like the stock one -- a mirror, or one built on it -- may or may not
  // carry a client, and the server cannot know. An image of somebody's own
  // gets no line at all: nagging about it would teach them to ignore this
  // line.
  //
  // Only the shape is decided here. Which tags this build publishes is the
  // server's answer, and the review step shows it.
  const DOCKER_IMAGE = 'ghcr.io/eyupio/zoomies-runner-docker';
  const STOCK_TAGGED = /^ghcr\.io\/eyupio\/zoomies-runner(:[^@]+)?$/;
  const STOCK_DIGEST = /^ghcr\.io\/eyupio\/zoomies-runner(:[^@]+)?@/;
  const LOOKALIKE_IMAGE = /(^|\/)zoomies-runner(:|$)/;

  const imageNotice = $derived.by((): string | undefined => {
    if (!usesImage || (draft.docker_mode ?? 'none') === 'none') return undefined;
    const image = draft.image?.trim() ?? '';
    if (image === '') return undefined;
    const tag = image.includes(':') ? image.slice(image.indexOf(':')) : '';
    if (STOCK_DIGEST.test(image)) {
      return `A digest names one exact image, and this one has no Docker client. Pin a digest of ${DOCKER_IMAGE} instead.`;
    }
    if (STOCK_TAGGED.test(image)) {
      return `The stock runner image has no Docker client, so this pool runs ${DOCKER_IMAGE}${tag} instead where that tag exists for it. The review step shows the image it kept.`;
    }
    if (LOOKALIKE_IMAGE.test(image)) {
      return 'This looks like the stock runner image, which has no Docker client, and it is not one Zoomies switches for you. Jobs that run docker fail on it even with a daemon attached, unless docker is installed in it.';
    }
    return undefined;
  });

  function chooseBackend(value: string): void {
    draft.backend = value as BackendKind;
    touch('backend');
  }

  function chooseDockerMode(value: string): void {
    const next = value as DockerMode;
    // Choosing the socket again is a fresh decision, so it needs fresh consent.
    if (next === 'host-socket' && draft.docker_mode !== 'host-socket') socketConfirmed = false;
    draft.docker_mode = next;
    touch('docker_mode');
  }
</script>

{#if usesImage}
  <Field
    label="Operating system"
    error={errors['platform.os'] ?? errors['platform.os_version']}
    hint="Picks the zoomies-runner image these runners boot, and keeps them off hosts running something else."
  >
    {#snippet children({ id, describedBy, invalid })}
      <Select
        value={chosenOS}
        options={osOptions}
        {id}
        {describedBy}
        {invalid}
        onchange={chooseOS}
      />
    {/snippet}
  </Field>

  <p class="platform-note">
    {#if effectiveImage}
      Runners boot <code>{effectiveImage}</code>.
    {:else}
      Runners boot the controller's default image.
    {/if}
    {#if hostsKnown}
      {matchingHosts === 0
        ? ' No connected host matches, so this pool would never place a runner.'
        : ` ${pluralise(matchingHosts, 'connected host')} match.`}
    {/if}
  </p>
{/if}

<RadioGroup
  name="pool-backend"
  legend="Backend"
  value={draft.backend}
  options={backendOptions}
  onchange={chooseBackend}
/>

{#if unavailable}
  <!--
    A pool no host can run is the failure that looks like health: it is enabled,
    its labels match, and it never makes a runner. The wizard will not create
    one while the fleet has something else to offer, so this says what is wrong
    and changes it in one click rather than leaving the operator to guess.
  -->
  <div class="unrunnable" role="group" aria-labelledby="backend-unrunnable">
    <p class="unrunnable-title" id="backend-unrunnable">
      <ServerOff size={16} aria-hidden="true" />
      No {restricted ? 'matching' : 'connected'} host can run a {backendLabel(draft.backend)} pool
    </p>
    <p class="unrunnable-body"><RemedyText text={unavailable} /></p>
    <div class="unrunnable-actions">
      {#each runnable as entry (entry.kind)}
        <Button size="sm" onclick={() => chooseBackend(entry.kind)}>
          Use {backendLabel(entry.kind)} ({pluralise(entry.hosts, 'host')})
        </Button>
      {/each}
    </div>
  </div>
{/if}

{#if usesImage}
  <Field
    label="Image"
    error={errors['image']}
    hint="Override the image the operating system above selects. Leave it empty unless you build your own."
    notice={imageNotice}
  >
    {#snippet children({ id, describedBy, invalid })}
      <Input
        bind:value={draft.image}
        {id}
        {describedBy}
        {invalid}
        mono
        placeholder={chosen?.image ?? 'ghcr.io/eyupio/zoomies-runner:latest'}
        autocomplete="off"
        onblur={() => touch('image')}
      />
    {/snippet}
  </Field>
{/if}

<Field
  label="Runner version"
  error={errors['runner_version']}
  hint="Pin the GitHub Actions runner version, or leave it empty to track the latest."
>
  {#snippet children({ id, describedBy, invalid })}
    <Input
      bind:value={draft.runner_version}
      {id}
      {describedBy}
      {invalid}
      mono
      placeholder="latest"
      autocomplete="off"
      onblur={() => touch('runner_version')}
    />
  {/snippet}
</Field>

<div class="docker">
  <RadioGroup
    name="pool-docker-mode"
    legend="Docker in jobs"
    value={draft.docker_mode}
    options={dockerOptions}
    onchange={chooseDockerMode}
  />

  {#if draft.docker_mode === 'host-socket'}
    <div class="danger" role="group" aria-labelledby="host-socket-warning">
      <p class="danger-title" id="host-socket-warning">
        <ShieldAlert size={16} aria-hidden="true" />
        Any job on this pool can become root on the host
      </p>
      <p class="danger-body">
        Mounting the host's Docker socket lets a job start a privileged container, mount the host
        filesystem and read every secret on that machine — including the credentials of every other
        pool's runners. A pull request from a fork is enough to do it. Use Docker in Docker unless
        you control every workflow that can reach these labels.
      </p>
      <Checkbox
        bind:checked={socketConfirmed}
        label="I understand that this gives every job on this pool root on the host"
        describedBy={dockerModeErrorId}
        onchange={() => touch('docker_mode')}
      />
      {#if errors['docker_mode']}
        <!-- role="alert" so the failure is spoken when it appears; the
             describedby link on the checkbox alone is only read if the field
             is revisited. -->
        <p class="danger-error" id={dockerModeErrorId} role="alert">{errors['docker_mode']}</p>
      {/if}
    </div>
  {/if}
</div>

<Checkbox
  bind:checked={draft.run_as_root}
  label="Run jobs as root inside the runner"
  description="Convenient for installing packages mid-job, and it means a compromised job owns the whole runner. Leave it off unless a workflow genuinely needs it."
  onchange={() => touch('run_as_root')}
/>

<style>
  .platform-note {
    margin: calc(var(--z-space-3) * -1) 0 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .platform-note code {
    font-family: var(--z-font-mono);
  }
  .unrunnable {
    margin-top: var(--z-space-4);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-danger-border, var(--z-border));
    border-left: var(--z-border-width-rail) solid var(--z-danger);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
  }
  .unrunnable-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .unrunnable-body {
    margin: var(--z-space-2) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .unrunnable-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin-top: var(--z-space-3);
  }

  .docker {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .danger {
    padding: var(--z-space-4);
    border: var(--z-border-width-thick) solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
  }
  .danger-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-bold);
    color: var(--z-danger);
  }
  .danger-body {
    margin: var(--z-space-2) 0 var(--z-space-3);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .danger-error {
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-danger);
  }
</style>
