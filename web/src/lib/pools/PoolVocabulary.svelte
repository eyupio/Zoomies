<!--
  The words the pool pages share: backend and Docker-mode names, the wizard's
  steps, and the human label for every field the API can reject.

  This file has no markup on purpose. The wizard, the pools grid and the pool
  detail page all need the same vocabulary, and an operator who reads "Host
  socket" in a table and then "host-socket" in a wizard has to work out that
  they are the same thing. Keeping the strings here means they cannot drift --
  and it keeps the wizard's steps importable without the steps importing the
  wizard back.
-->
<script module lang="ts">
  import type { BackendKind, DockerMode, Host } from '$lib/api/types';
  import { pluralise } from '$lib/format';

  export interface Choice<T> {
    value: T;
    label: string;
    /** One line: what choosing this actually means for a job. */
    consequence: string;
  }

  export const BACKENDS: readonly Choice<BackendKind>[] = [
    {
      value: 'docker',
      label: 'Docker',
      consequence: 'Each runner is a container, thrown away when the job ends.',
    },
    {
      value: 'podman',
      label: 'Podman',
      consequence: 'The same as Docker, without a root daemon on the host.',
    },
    {
      value: 'process',
      label: 'Process',
      consequence:
        'The runner is a plain process on the host, so a job can see and change the host filesystem.',
    },
  ];

  export const DOCKER_MODES: readonly Choice<DockerMode>[] = [
    {
      value: 'none',
      label: 'None',
      consequence: 'Jobs cannot use Docker. The safe default.',
    },
    {
      value: 'dind',
      label: 'Docker in Docker',
      consequence: 'Each job gets a private, privileged Docker daemon of its own.',
    },
    {
      value: 'host-socket',
      label: 'Host socket',
      consequence:
        "Jobs share the host's Docker daemon, which means any job on this pool can become root on the host.",
    },
  ];

  export function backendLabel(kind: BackendKind | undefined): string {
    return BACKENDS.find((b) => b.value === kind)?.label ?? 'Not set';
  }

  export function dockerModeLabel(mode: DockerMode | undefined): string {
    return DOCKER_MODES.find((m) => m.value === (mode ?? 'none'))?.label ?? 'None';
  }

  /** What the fleet can actually run right now, counted from the connected hosts. */
  export interface BackendOffer {
    kind: BackendKind;
    /** Hosts where this backend is available. */
    hosts: number;
    /** Hosts where Docker in Docker is possible. */
    dindHosts: number;
    /** The first host's explanation of why it is unavailable, when there is one. */
    detail?: string;
  }

  export function backendOffers(hosts: readonly Host[]): BackendOffer[] {
    const kinds: BackendKind[] = ['docker', 'podman', 'process'];
    return kinds.map((kind) => {
      let available = 0;
      let dind = 0;
      let detail: string | undefined;
      for (const host of hosts) {
        const info = (host.backend_info ?? []).find((entry) => entry.kind === kind);
        const listed = info?.available === true || (host.backends ?? []).includes(kind);
        if (listed) available += 1;
        if (listed && info?.supports_dind === true) dind += 1;
        if (!listed && detail === undefined && info?.detail) detail = info.detail;
      }
      const offer: BackendOffer = { kind, hosts: available, dindHosts: dind };
      if (detail !== undefined) offer.detail = detail;
      return offer;
    });
  }

  /**
   * Why this backend cannot be chosen, or "" when it can.
   *
   * A pool whose backend no host offers never makes a runner and looks perfectly
   * healthy doing it, so the wizard refuses to create one while the fleet has
   * something else to offer. The escape hatch is deliberate: when nothing is
   * connected, or nothing is offering anything, there is no better answer to
   * insist on and the pool is allowed through with a warning -- which is how the
   * first pool gets created before the first agent joins.
   */
  export function backendUnavailable(
    backend: BackendKind,
    offers: readonly BackendOffer[],
    hostsKnown: boolean,
  ): string {
    if (!hostsKnown) return '';
    const chosen = offers.find((offer) => offer.kind === backend);
    if (!chosen || chosen.hosts > 0) return '';
    const others = offers.filter((offer) => offer.kind !== backend && offer.hosts > 0);
    if (others.length === 0) return '';
    const alternatives = others
      .map((offer) => `${backendLabel(offer.kind)} (${pluralise(offer.hosts, 'host')})`)
      .join(' or ');
    const because = chosen.detail ? ` ${chosen.detail}` : '';
    return `No connected host offers ${backendLabel(backend)}, so this pool would never start a runner.${because} Choose ${alternatives}, or make ${backendLabel(backend)} work on a host first.`;
  }

  /* -- the creation wizard ------------------------------------------------- */

  export interface WizardStepDef {
    id: string;
    title: string;
    description: string;
  }

  export const WIZARD_STEPS: readonly WizardStepDef[] = [
    {
      id: 'target',
      title: 'Target',
      description: 'Which GitHub installation these runners register with.',
    },
    {
      id: 'labels',
      title: 'Labels',
      description: 'What a workflow writes in runs-on to reach this pool.',
    },
    { id: 'backend', title: 'Backend', description: 'How a runner is actually run on a host.' },
    { id: 'scaling', title: 'Scaling', description: 'How many runners, and for how long.' },
    { id: 'review', title: 'Review', description: 'What the controller makes of it.' },
  ];

  /** Which step each field belongs to, so a server error can point at the right one. */
  export const STEP_FIELDS: readonly (readonly string[])[] = [
    ['name', 'installation_id', 'runner_group'],
    ['labels'],
    ['backend', 'image', 'runner_version', 'docker_mode', 'run_as_root'],
    [
      'min_runners',
      'max_runners',
      'idle_timeout',
      'ephemeral',
      'resources.cpus',
      'resources.memory_mb',
      'resources.disk_gb',
    ],
    [],
  ];

  /** Human labels for the API's field names, used when the server rejects a field. */
  export const FIELD_LABELS: Readonly<Record<string, string>> = {
    name: 'Name',
    installation_id: 'GitHub installation',
    runner_group: 'Runner group',
    labels: 'Labels',
    backend: 'Backend',
    image: 'Image',
    runner_version: 'Runner version',
    min_runners: 'Minimum runners',
    max_runners: 'Maximum runners',
    idle_timeout: 'Idle timeout',
    ephemeral: 'Runner lifetime',
    docker_mode: 'Docker in jobs',
    run_as_root: 'Run as root',
    'resources.cpus': 'CPUs',
    'resources.memory_mb': 'Memory',
    'resources.disk_gb': 'Disk',
    host_selector: 'Host selector',
    env: 'Environment',
  };

  /** The step a field lives on, or the review step when we do not recognise it. */
  export function stepForField(field: string): number {
    const index = STEP_FIELDS.findIndex((fields) => fields.includes(field));
    return index === -1 ? WIZARD_STEPS.length - 1 : index;
  }
</script>
