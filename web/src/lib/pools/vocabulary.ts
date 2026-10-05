/**
 * The words the pool pages share: backend and Docker-mode names, the wizard's
 * steps, and the human label for every field the API can reject.
 *
 * This file has no markup on purpose. The wizard, the pools grid and the pool
 * detail page all need the same vocabulary, and an operator who reads "Host
 * socket" in a table and then "host-socket" in a wizard has to work out that
 * they are the same thing. Keeping the strings here means they cannot drift --
 * and it keeps the wizard's steps importable without the steps importing the
 * wizard back.
 */
import type { BackendKind, DockerMode, Host, Platform } from '$lib/api/types';
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
    consequence:
      'Jobs get no Docker daemon, so a docker step, a container: or a services: block fails on this pool. The safe default.',
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

/* -- platforms ----------------------------------------------------------- */

/**
 * How an operating system is spelled in prose. The API sends the canonical
 * lowercase form; this is the only place that decides it reads "macOS" and
 * not "macos".
 */
const OS_NAMES: Readonly<Record<string, string>> = {
  ubuntu: 'Ubuntu',
  debian: 'Debian',
  fedora: 'Fedora',
  rocky: 'Rocky Linux',
  alpine: 'Alpine',
  macos: 'macOS',
  windows: 'Windows',
};

export function osLabel(os: string | undefined): string {
  if (!os) return '';
  return OS_NAMES[os] ?? os;
}

/**
 * A platform as one line: "Ubuntu 24.04, arm64". Empty fields are left out
 * rather than filled with "any", because a pool that says only "arm64" is
 * making exactly one promise and the line should read as one.
 */
export function platformLabel(platform: Platform | undefined): string {
  const parts: string[] = [];
  const os = osLabel(platform?.os);
  if (os) parts.push(platform?.os_version ? `${os} ${platform.os_version}` : os);
  if (platform?.arch) parts.push(platform.arch);
  return parts.join(', ');
}

/** The same, but with the word an empty platform deserves. */
export function platformLabelOrAny(platform: Platform | undefined): string {
  return platformLabel(platform) || 'Any host';
}

/** The value a platform select uses: "ubuntu-24.04", or "" for "any". */
export function platformKey(os: string | undefined, version: string | undefined): string {
  if (!os) return '';
  return version ? `${os}-${version}` : os;
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
  /** True when the offers were counted over a pool's selected hosts only. */
  restricted = false,
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
  // Once a pool is kept to some of the fleet, "no connected host offers it"
  // is false as often as it is true -- the daemon may be running happily on
  // the machines this pool is not allowed to use. Say which set was counted.
  const nobody = restricted
    ? `No matching host offers ${backendLabel(backend)}`
    : `No connected host offers ${backendLabel(backend)}`;
  const fix = restricted
    ? `, widen which hosts this pool may use, or make ${backendLabel(backend)} work on one of them first.`
    : `, or make ${backendLabel(backend)} work on a host first.`;
  return `${nobody}, so this pool would never start a runner.${because} Choose ${alternatives}${fix}`;
}

/* -- the creation wizard ------------------------------------------------- */

export interface WizardStepDef {
  id: string;
  title: string;
  description: string;
}

/**
 * The two ways a pool is made.
 *
 * `simple` is the pool most fleets want and could not previously ask for:
 * named, labelled, and then left alone -- its runners are sized by whichever
 * host each one lands on, it may use any host that can run it, and every
 * timing follows the fleet. Nothing on that path is a number an operator has
 * to have an opinion about, because every one of them has a right answer the
 * controller already knows.
 *
 * `advanced` is the same pool with the opinions put back: a fixed size, a
 * host selector, a backend and platform chosen by hand, and the runner
 * timings this pool disagrees with the fleet about.
 *
 * It is a fork rather than a page of collapsed sections because the two
 * audiences want different things from the same screen. Someone adding their
 * first pool wants to be finished; someone tuning a Windows pool's provision
 * timeout wants every control in front of them, and neither is served by
 * making the other's path longer.
 */
export type WizardMode = 'simple' | 'advanced';

/** Every step the wizard has, by id. The order is in the two lists below. */
const STEP_DEFS: Readonly<Record<StepId, WizardStepDef>> = {
  mode: {
    id: 'mode',
    title: 'Setup',
    description: 'How much of this pool you want to decide.',
  },
  target: {
    id: 'target',
    title: 'Target',
    description: 'Which GitHub installation these runners register with.',
  },
  labels: {
    id: 'labels',
    title: 'Labels',
    description: 'What a workflow writes in runs-on to reach this pool.',
  },
  hosts: { id: 'hosts', title: 'Hosts', description: 'Which machines these runners land on.' },
  backend: {
    id: 'backend',
    title: 'Backend',
    description: 'What a runner is made of, and how it is run on a host.',
  },
  // Size before count, because a maximum means nothing until it is known
  // what one runner costs on the machines it will land on.
  size: {
    id: 'size',
    title: 'Size',
    description: 'How much machine one runner gets, its scratch space and its cache.',
  },
  scaling: {
    id: 'scaling',
    title: 'Scaling',
    description: 'How many runners, and for how long.',
  },
  runners: {
    id: 'runners',
    title: 'Runners',
    description: 'The timings this pool disagrees with the fleet about.',
  },
  // Only the simple path walks this one. The advanced path asks the same
  // question on its backend step, beside the image and the host socket the
  // simple path does not offer.
  docker: {
    id: 'docker',
    title: 'Docker',
    description: 'Whether jobs here build container images.',
  },
  review: { id: 'review', title: 'Review', description: 'What the controller makes of it.' },
};

/** Which fields live on which step, so a server error can point at the right one. */
const STEP_FIELDS_BY_ID: Readonly<Record<StepId, readonly string[]>> = {
  mode: [],
  docker: ['docker_mode'],
  target: ['name', 'installation_id', 'runner_group'],
  labels: ['labels'],
  hosts: ['host_selector'],
  backend: [
    'backend',
    'platform.os',
    'platform.os_version',
    'platform.arch',
    'image',
    'runner_version',
    'docker_mode',
    'run_as_root',
  ],
  size: [
    'size_from_profile',
    'resources.cpus',
    'resources.memory_mb',
    'resources.disk_gb',
    'resources.pids_limit',
    'cache.enabled',
    'cache.scope',
    'cache.size_limit',
    'cache.source',
    'cache.repository',
    'tmpfs.work.enabled',
    'tmpfs.work.size_mb',
    'tmpfs.tmp.enabled',
    'tmpfs.tmp.size_mb',
    'tmpfs.daemon.enabled',
    'tmpfs.daemon.size_mb',
  ],
  scaling: [
    'min_runners',
    'max_runners',
    'priority',
    'idle_timeout',
    'ephemeral',
    'no_default_labels',
  ],
  runners: [
    'runner_settings.provision_timeout',
    'runner_settings.drain_timeout',
    'runner_settings.max_runner_lifetime',
    'runner_settings.scale_up_delay',
    'runner_settings.docker_wait',
  ],
  review: [],
};

export type StepId =
  | 'mode'
  | 'target'
  | 'labels'
  | 'docker'
  | 'hosts'
  | 'backend'
  | 'size'
  | 'scaling'
  | 'runners'
  | 'review';

// The simple path asks four things, and the fourth is the one an operator
// cannot discover anywhere else: a pool whose jobs build images needs a
// daemon, and nothing in a name or a label says so. Leaving it to the
// advanced path meant the only way to build an image was to know that
// "advanced" was where Docker lived.
const SIMPLE_STEP_IDS: readonly StepId[] = ['mode', 'target', 'labels', 'docker', 'review'];
const ADVANCED_STEP_IDS: readonly StepId[] = [
  'mode',
  'target',
  'labels',
  'hosts',
  'backend',
  'size',
  'scaling',
  'runners',
  'review',
];

/*
 * The fork is a creation question, so an edit does not walk it.
 *
 * An operator opening a pool they already have is there to change one
 * setting, and "how much of this pool do you want to decide" is not a
 * question about that -- the pool has already answered it, and the wizard
 * reads the answer off the pool to pick which path to open. Nothing is lost
 * in either direction: on the advanced path the size step still offers the
 * host's share, the hosts step the whole fleet, and the runner timings can
 * still be cleared; on the simple path every step offers the advanced one.
 * That second half matters more than it looks, because a plain pool is
 * exactly the pool whose elastic CPU, host selector or fixed size has never
 * been set, and the fork's absence must not be what keeps it that way.
 */
function stepIds(mode: WizardMode, editing: boolean): readonly StepId[] {
  const ids = mode === 'simple' ? SIMPLE_STEP_IDS : ADVANCED_STEP_IDS;
  return editing ? ids.filter((id) => id !== 'mode') : ids;
}

/** The steps this mode walks, in order. */
export function wizardSteps(mode: WizardMode, editing = false): readonly WizardStepDef[] {
  return stepIds(mode, editing).map((id) => STEP_DEFS[id]);
}

/** The fields each of this mode's steps owns, indexed the same way. */
export function stepFields(mode: WizardMode, editing = false): readonly (readonly string[])[] {
  return stepIds(mode, editing).map((id) => STEP_FIELDS_BY_ID[id]);
}

/** Where a step sits on this path, or -1 when the path does not walk it. */
export function stepIndex(id: StepId, mode: WizardMode, editing = false): number {
  return stepIds(mode, editing).indexOf(id);
}

/** Human labels for the API's field names, used when the server rejects a field. */
export const FIELD_LABELS: Readonly<Record<string, string>> = {
  name: 'Name',
  installation_id: 'GitHub installation',
  runner_group: 'Runner group',
  labels: 'Labels',
  backend: 'Backend',
  'platform.os': 'Operating system',
  'platform.os_version': 'Release',
  'platform.arch': 'Architecture',
  image: 'Image',
  runner_version: 'Runner version',
  min_runners: 'Minimum runners',
  max_runners: 'Maximum runners',
  idle_timeout: 'Idle timeout',
  ephemeral: 'Runner lifetime',
  no_default_labels: 'Default labels',
  docker_mode: 'Docker in jobs',
  run_as_root: 'Run as root',
  priority: 'Priority',
  size_from_profile: 'Size from the host',
  'resources.cpus': 'CPU per runner',
  'resources.memory_mb': 'Memory per runner',
  'resources.disk_gb': 'Disk per runner',
  'resources.pids_limit': 'Process limit',
  'cache.enabled': 'Cache',
  'cache.scope': 'Cache isolation scope',
  'cache.size_limit': 'Cache size limit',
  'cache.source': 'Cache host path',
  'cache.repository': 'Cache repository',
  'tmpfs.work.enabled': 'Work folder in memory',
  'tmpfs.work.size_mb': 'Work folder size',
  'tmpfs.tmp.enabled': '/tmp in memory',
  'tmpfs.tmp.size_mb': '/tmp size',
  'tmpfs.daemon.enabled': 'Docker image store in memory',
  'tmpfs.daemon.size_mb': 'Docker image store size',
  host_selector: 'Hosts',
  env: 'Environment',
  'runner_settings.provision_timeout': 'Provision timeout',
  'runner_settings.drain_timeout': 'Drain timeout',
  'runner_settings.max_runner_lifetime': 'Maximum runner lifetime',
  'runner_settings.scale_up_delay': 'Scale-up delay',
  'runner_settings.docker_wait': 'Docker wait',
};

/**
 * The step a field lives on in this mode, or the review step when we do not
 * recognise it -- which is also where a field belonging to a step the simple
 * path does not walk lands, because the review step is the one screen that
 * shows every error at once.
 */
export function stepForField(field: string, mode: WizardMode, editing = false): number {
  const fields = stepFields(mode, editing);
  const index = fields.findIndex((owned) => owned.includes(field));
  return index === -1 ? fields.length - 1 : index;
}
