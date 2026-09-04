<!--
  Step three: how a runner is actually run, and how much Docker a job gets.

  The two dangerous answers in the whole wizard live here, so both are spelled
  out in the consequence rather than in a footnote, and the host-socket option
  cannot be left selected without a deliberate confirmation.
-->
<script lang="ts">
  import { ShieldAlert } from '@lucide/svelte';
  import type { BackendKind, DockerMode } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import { BACKENDS, DOCKER_MODES } from './PoolVocabulary.svelte';
  import type { BackendOffer, PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    offers: readonly BackendOffer[];
    /** False until the fleet cache has landed, so we do not cry wolf about hosts. */
    hostsKnown: boolean;
    socketConfirmed?: boolean;
  }

  let {
    draft,
    errors,
    touch,
    offers,
    hostsKnown,
    socketConfirmed = $bindable(false),
  }: Props = $props();

  function offer(kind: BackendKind): BackendOffer | undefined {
    return offers.find((entry) => entry.kind === kind);
  }

  function availability(kind: BackendKind): string {
    if (!hostsKnown) return '';
    const found = offer(kind);
    if (!found) return '';
    if (found.hosts === 0) {
      return found.detail
        ? `No connected host offers it: ${found.detail}`
        : 'No connected host offers it, so this pool would never place a runner.';
    }
    return `Offered by ${pluralise(found.hosts, 'connected host')}.`;
  }

  const backendOptions = $derived(
    BACKENDS.map((choice) => ({
      value: choice.value,
      label: choice.label,
      description: `${choice.consequence} ${availability(choice.value)}`.trim(),
    })),
  );

  const dindHosts = $derived(offer(draft.backend)?.dindHosts ?? 0);

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

<RadioGroup
  name="pool-backend"
  legend="Backend"
  value={draft.backend}
  options={backendOptions}
  onchange={chooseBackend}
/>

{#if usesImage}
  <Field
    label="Image"
    error={errors['image']}
    hint="The container image runners are built from. Leave it empty to use the controller's default."
  >
    {#snippet children({ id, describedBy, invalid })}
      <Input
        bind:value={draft.image}
        {id}
        {describedBy}
        {invalid}
        mono
        placeholder="ghcr.io/actions/actions-runner:latest"
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
        onchange={() => touch('docker_mode')}
      />
      {#if errors['docker_mode']}
        <p class="danger-error">{errors['docker_mode']}</p>
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
  .docker {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .danger {
    padding: var(--z-space-4);
    border: 2px solid var(--z-danger-border);
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
