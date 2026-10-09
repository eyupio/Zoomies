/**
 * The words the pool pages share: backend and Docker-mode names, and the human
 * label for every field the API can reject.
 *
 * This file has no markup on purpose. The editor, the pools grid and the pool
 * detail page all need the same vocabulary, and an operator who reads "Host
 * socket" in a table and then "host-socket" in the editor has to work out that
 * they are the same thing. Keeping the strings here means they cannot drift --
 * and it keeps the editor's sections importable without the sections importing
 * the editor back.
 */
import type { BackendKind, DockerMode, Host, MemoryBurstPolicy, Platform } from '$lib/api/types';
import { pluralise } from '$lib/format';
import { memoryLabel } from './sizing';

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
 * healthy doing it, so the editor refuses to create one while the fleet has
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
  provider_selector: 'Providers',
  env: 'Environment',
  'runner_settings.provision_timeout': 'Provision timeout',
  'runner_settings.drain_timeout': 'Drain timeout',
  'runner_settings.max_runner_lifetime': 'Maximum runner lifetime',
  'runner_settings.scale_up_delay': 'Scale-up delay',
  'runner_settings.docker_wait': 'Docker wait',
};

/**
 * What a pool's memory valve is set to, in a line: the mode, the most a runner
 * may hold and the swap it may fall back on. The pool page and the CLI say it the
 * same way, so an operator reading one finds the other.
 */
export function memoryBurstLabel(policy: MemoryBurstPolicy | undefined): string {
  if (policy?.mode === 'observe') return 'Observe only';
  if (policy?.mode !== 'automatic') return 'Off';
  const ceiling =
    (policy.max_memory_mb ?? 0) > 0
      ? `up to ${memoryLabel(policy.max_memory_mb ?? 0)} per runner`
      : 'up to half as much again as a runner starts with';
  const swap =
    (policy.spill_mb ?? 0) > 0
      ? `, with up to ${memoryLabel(policy.spill_mb ?? 0)} of swap as the last resort`
      : '';
  return `Automatic, ${ceiling}${swap}`;
}
