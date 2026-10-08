<!--
  The first section, and the only one a new pool has to read: what it is
  called, what a workflow writes to reach it, whether its jobs build container
  images, and which GitHub account it registers runners with.

  A fleet's first pool needs exactly these, and nothing else on the page has an
  answer the controller does not already know. Docker is here because it is the
  one question a fleet cannot answer for a pool -- nothing in a name, a label or
  a host says whether the jobs behind it run `docker build` -- and until it was
  asked on the first screen, the only way to turn it on was to know that it
  lived three steps in.

  It is a yes or no rather than the three-way setting, because the third answer
  hands every job on the pool root on the host. That one stays with the runner's
  settings, where it is chosen deliberately and confirmed.

  With no GitHub installation there is nothing to register against, so this
  says that plainly and links to the page that fixes it, rather than offering an
  empty select the operator would poke at. With exactly one -- the usual case --
  it is chosen already and shown as a line, because a control with one answer is
  a question nobody has to be asked.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Dices, Plug } from '@lucide/svelte';
  import type { DockerMode, Installation, RunnerGroup } from '$lib/api/types';
  import { BRAND_LABEL, brandLabels, brandedLabel, brandedName, isImplicit } from '$lib/brand';
  import { joinWords } from '$lib/format';
  import { installationStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import LabelInput from './LabelInput.svelte';
  import PoolMore from './PoolMore.svelte';
  import RunsOnPreview from './RunsOnPreview.svelte';
  import type { PoolDraft } from './draft';
  import type { SectionId } from './sections';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    editing: boolean;
    installations: readonly Installation[];
    loading: boolean;
    error: unknown;
    /** Fetch the installations again after a failure. */
    onretry?: () => void;
    groups: readonly RunnerGroup[];
    groupsLoading: boolean;
    groupsError: unknown;
    /** Roll another name. Left out when editing, where a name is already in use. */
    onspin?: () => void;
    /** False until the fleet has been asked, so no claim is made about it. */
    hostsKnown: boolean;
    /** How many hosts say they can run a Docker daemon beside a runner. */
    dindHosts: number;
    /** Open another section: the host socket is chosen there, not here. */
    onopen: (id: SectionId) => void;
  }

  let {
    draft,
    errors,
    touch,
    editing,
    installations,
    loading,
    error,
    onretry,
    groups,
    groupsLoading,
    groupsError,
    onspin,
    hostsKnown,
    dindHosts,
    onopen,
  }: Props = $props();

  /* -- the name ------------------------------------------------------------- */

  // Purely so the dice turn when they are rolled. Cleared on animationend
  // rather than on a timer, so the two cannot disagree about how long the
  // animation is -- and under prefers-reduced-motion, where the token is 1ms,
  // it ends immediately and nothing spins.
  let rolling = $state(false);

  function spin(): void {
    rolling = true;
    onspin?.();
  }

  /* -- the installation ----------------------------------------------------- */

  const installationOptions = $derived(
    installations.map((entry) => ({
      value: entry.id ?? '',
      label: `${entry.target ?? 'unnamed'} (${entry.target_type ?? 'org'})`,
    })),
  );
  const chosen = $derived(installations.find((entry) => entry.id === draft.installation_id));
  // One installation is chosen for the operator as soon as it is known, and an
  // edit keeps the one the pool has: either way there is nothing to ask. The
  // select comes back the moment the choice is real, or the pool names one
  // this list does not have.
  const onlyOne = $derived(!editing && installations.length === 1 && chosen !== undefined);

  const groupOptions = $derived([
    { value: '', label: 'Default' },
    ...groups.map((group) => ({ value: group.name ?? '', label: group.name ?? 'unnamed' })),
  ]);
  // A runner group is a setting for an organisation that has made some. With
  // none beyond the default there is nothing to choose between, and a select
  // offering one answer is noise on the screen a first pool is made on.
  // Open already when the pool names one, or the field is refused: a setting
  // that is in use is never hidden behind a closed row.
  let groupOpen = $state(
    untrack(() => draft.runner_group !== '' || Boolean(errors['runner_group'])),
  );
  const showGroup = $derived(
    groupsLoading ||
      groups.length > 0 ||
      draft.runner_group !== '' ||
      Boolean(errors['runner_group']),
  );

  /* -- the labels ----------------------------------------------------------- */

  const duplicated = $derived(draft.labels.filter((label) => isImplicit(label)));

  // Every pool answers to the brand, whether or not it is typed here: the
  // server adds it on save. Saying so is the honest thing to do, and it is what
  // "runs-on: zoomies" -- any runner in this fleet -- resolves to.
  const carriesBrand = $derived(
    draft.labels.some((label) => label.trim().toLowerCase() === BRAND_LABEL),
  );

  // The label this pool's name suggests. It is only offered while the operator
  // has not already given the pool a label of its own, so it never nags.
  const suggestion = $derived(brandedLabel(draft.name));
  const showSuggestion = $derived(
    draft.name.trim() !== '' &&
      suggestion !== BRAND_LABEL &&
      !draft.labels.some((label) => label.trim().toLowerCase() === suggestion),
  );

  function useSuggestion(): void {
    draft.labels = [...draft.labels, suggestion];
    touch('labels');
  }

  /* -- Docker ---------------------------------------------------------------- */

  const socket = $derived(draft.docker_mode === 'host-socket');
  const processRunner = $derived(draft.backend === 'process');
  const wantsDocker = $derived(draft.docker_mode === 'dind' ? 'dind' : 'none');

  const dockerOptions = $derived([
    {
      value: 'none',
      label: 'No',
      description: 'Most pools never build an image, and a runner without a daemon starts faster.',
    },
    {
      value: 'dind',
      label: 'Yes, give each runner a Docker daemon',
      description:
        hostsKnown && dindHosts === 0
          ? 'For jobs that run docker build, or that use a container: or services: block. No connected host reports that it can do this.'
          : 'For jobs that run docker build, or that use a container: or services: block.',
    },
  ]);

  function chooseDocker(value: string): void {
    draft.docker_mode = value as DockerMode;
    touch('docker_mode');
  }
</script>

<Field
  label="Pool name"
  required
  error={errors['name']}
  hint={onspin
    ? 'Suggested from your fleet. Type over it, or roll the dice for another. Names start with zoomies-.'
    : 'Shown in runner names, the audit log and the CLI. Names start with zoomies-.'}
>
  {#snippet children({ id, describedBy, invalid })}
    <Input
      bind:value={draft.name}
      {id}
      {describedBy}
      {invalid}
      mono
      placeholder="zoomies-linux-x64"
      autocomplete="off"
      onblur={() => {
        // Branded here as well as on save, so the field shows the name the
        // server will store rather than the one that was typed.
        draft.name = brandedName(draft.name);
        touch('name');
      }}
    >
      {#snippet trailing()}
        {#if onspin}
          <span class="dice" class:rolling onanimationend={() => (rolling = false)}>
            <IconButton icon={Dices} label="Spin a new name" size="sm" onclick={spin} />
          </span>
        {/if}
      {/snippet}
    </Input>
  {/snippet}
</Field>

<div class="labels">
  <Field
    label="Labels"
    required
    error={errors['labels']}
    hint="What a workflow lists in runs-on to reach this pool. Press Enter or a comma after each one."
  >
    {#snippet children({ id, describedBy, invalid })}
      <!-- A getter and a setter rather than a bound property: the labels are an
           array held by the editor, this component sits under the section's own
           row, and Svelte's ownership check is right to be wary of a binding that
           reaches through a component that never declared it. -->
      <LabelInput
        bind:value={() => draft.labels, (labels) => (draft.labels = labels)}
        {id}
        {describedBy}
        {invalid}
        onblur={() => touch('labels')}
      />
    {/snippet}
  </Field>

  {#if showSuggestion}
    <p class="suggest">
      <button type="button" onclick={useSuggestion}>Use <code>{suggestion}</code></button>, the
      branded label this pool's name suggests.
    </p>
  {/if}

  <!-- The brand is what the server adds on save, so the preview shows it too. -->
  <RunsOnPreview labels={brandLabels(draft.labels)} />

  {#if !carriesBrand}
    <p class="echo">
      Every pool also answers to <code>{BRAND_LABEL}</code>, which Zoomies adds when the pool is
      saved. That is what a repository writes before anyone has decided which pool it belongs in.
    </p>
  {/if}

  {#if duplicated.length > 0}
    <p class="callout">
      GitHub already gives every self-hosted runner {joinWords(duplicated)}, so adding
      {duplicated.length === 1 ? 'it' : 'them'} here narrows nothing down. It is harmless, but the pool
      will look more specific than it is.
    </p>
  {/if}
</div>

{#if socket}
  <div class="callout" role="group" aria-label="Docker for jobs">
    <p>
      <strong>Docker for jobs: the host's socket.</strong> Jobs on this pool share the host's Docker
      daemon, which is chosen, and confirmed, under
      <button type="button" class="link" onclick={() => onopen('runner')}>Runner</button>.
    </p>
  </div>
{:else}
  <RadioGroup
    name="pool-wants-docker"
    legend="Will jobs here build container images?"
    value={wantsDocker}
    options={dockerOptions}
    disabled={processRunner}
    onchange={chooseDocker}
  />
  {#if processRunner}
    <p class="echo">
      A process runner has no daemon of its own to give. Choose Docker or Podman under
      <button type="button" class="link" onclick={() => onopen('runner')}>Runner</button> first.
    </p>
  {:else if wantsDocker === 'dind'}
    <p class="echo">
      The daemon runs in a <strong>privileged container</strong> beside each runner, and the two
      <strong>share one slot</strong> of whichever host they land on, so a host holds as many runners
      as of any other pool, each with a little less machine to itself. The runner image follows automatically:
      the published image plus a Docker client.
    </p>
  {/if}
{/if}

{#if error}
  <ErrorState {error} title="Installations could not be listed" {onretry} />
{:else if loading}
  <div class="loading">
    <Skeleton width="30%" height="0.75rem" />
    <Skeleton height="2rem" />
  </div>
{:else if installations.length === 0}
  <EmptyState
    icon={Plug}
    title="No GitHub connection yet"
    description="A pool registers its runners with a GitHub App installation, so Zoomies needs one before it can make a pool."
  >
    <Button variant="primary" href="/installations">Connect GitHub</Button>
  </EmptyState>
{:else if onlyOne && chosen}
  <p class="registers">
    <span>Registers runners with <strong>{chosen.target ?? 'your GitHub account'}</strong></span>
    <Badge status={installationStatus(chosen.healthy)} size="sm" />
  </p>
{:else}
  <Field
    label="GitHub installation"
    required
    error={errors['installation_id']}
    hint="Runners in this pool register against this organisation or repository."
  >
    {#snippet children({ id, describedBy, invalid })}
      <Select
        bind:value={draft.installation_id}
        options={installationOptions}
        placeholder="Choose an installation"
        {id}
        {describedBy}
        {invalid}
        required
        onchange={() => touch('installation_id')}
      />
    {/snippet}
  </Field>

  {#if chosen}
    <p class="chosen">
      <Badge status={installationStatus(chosen.healthy)} size="sm" />
      {#if chosen.healthy === false}
        <span
          >This installation last failed its check{chosen.last_error
            ? `: ${chosen.last_error}`
            : '.'} Runners registering against it are likely to fail until it is fixed.</span
        >
      {:else}
        <span
          >{chosen.pool_count ?? 0} other {(chosen.pool_count ?? 0) === 1
            ? 'pool uses'
            : 'pools use'}
          this installation.</span
        >
      {/if}
    </p>
  {/if}
{/if}

{#if !loading && !error && installations.length > 0 && showGroup}
  <PoolMore title="Runner group" note={draft.runner_group || 'Default'} bind:open={groupOpen}>
    <Field
      label="Runner group"
      error={errors['runner_group']}
      hint="Decides which repositories may use these runners. An organisation defaults to the zoomies group made when it was connected."
    >
      {#snippet children({ id, describedBy, invalid })}
        {#if groupsLoading}
          <Skeleton height="2rem" />
        {:else}
          <Select
            bind:value={draft.runner_group}
            options={groupOptions}
            {id}
            {describedBy}
            {invalid}
            onchange={() => touch('runner_group')}
          />
        {/if}
      {/snippet}
    </Field>
  </PoolMore>
{/if}

{#if groupsError}
  <p class="warn">
    Runner groups could not be listed, so only the default is offered. The pool can still be
    created; verify the installation to find out why GitHub refused.
  </p>
{/if}

<style>
  .dice {
    display: inline-flex;
  }
  .rolling {
    animation: roll var(--z-motion-slow) var(--z-ease);
  }
  @keyframes roll {
    from {
      transform: rotate(0turn);
    }
    to {
      transform: rotate(1turn);
    }
  }
  .labels {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .loading {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .suggest {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .suggest code,
  .echo code {
    padding: 0 var(--z-space-1);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-2xs);
  }
  .suggest button,
  .link {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    cursor: pointer;
    text-decoration: underline;
  }
  .registers,
  .chosen {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .registers strong {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }
  .chosen {
    font-size: var(--z-text-xs);
  }
  .warn {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-pending);
  }
</style>
