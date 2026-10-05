import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  draftErrors,
  draftFromPool,
  emptyDraft,
  poolIsTuned,
  toPoolBody,
} from '../src/lib/pools/draft.ts';
import type { PoolDraft } from '../src/lib/pools/draft.ts';
import type { Pool } from '../src/lib/api/types.ts';
import type { BackendOffer } from '../src/lib/pools/vocabulary.ts';

/** A draft that passes every rule the browser can check on its own. */
function valid(over: Partial<PoolDraft> = {}): PoolDraft {
  return {
    ...emptyDraft(),
    name: 'zoomies-linux-x64',
    installation_id: 'ins_1',
    labels: ['zoomies-linux-x64'],
    ...over,
  };
}

// The size is left to the host by *sending nothing*: absence is how the API is
// told, and the sliders keep a position of their own so that switching back to
// a fixed size does not lose what was chosen. If the position leaked into the
// request, a pool that said "the host decides" would be created at a size
// nobody on the page was looking at.
test('an automatic pool sends no size, whatever the sliders are holding', () => {
  const body = toPoolBody(valid({ sizing: 'automatic', cpus: '4', memory_mb: '8192' }));
  assert.deepEqual(body.resources, {});
  assert.equal(body.size_from_profile, false);
});

test('a fixed pool sends its figures, and a profile pool sends the flag instead', () => {
  const fixed = toPoolBody(valid({ sizing: 'fixed', cpus: '4', memory_mb: '8192' }));
  assert.equal(fixed.resources?.cpus, 4);
  assert.equal(fixed.resources?.memory_mb, 8192);
  assert.equal(fixed.size_from_profile, false);

  const profile = toPoolBody(valid({ sizing: 'profile', cpus: '4', memory_mb: '8192' }));
  assert.deepEqual(profile.resources, {});
  assert.equal(profile.size_from_profile, true);
});

// A minimum is a floor under either kind of size, and an empty field has to
// send nothing at all: the fleet reads that as "follow runners.minimum_*" live,
// so copying today's figure in would freeze it into the pool.
test('a minimum is sent only when one was given', () => {
  assert.deepEqual(toPoolBody(valid()).resources, {});
  const body = toPoolBody(valid({ min_cpus: '1', min_memory_mb: '2048' }));
  assert.equal(body.resources?.min_cpus, 1);
  assert.equal(body.resources?.min_memory_mb, 2048);
});

// The sidecar's shares mean nothing for a fixed size, where both containers get
// the whole figure, and nothing without a sidecar. CPU and memory are two shares
// that are sent on their own, and an even one is left out: absent is even.
test('the sidecar shares are sent only for a host-sized Docker-in-Docker pool, each on its own', () => {
  const sent = toPoolBody(
    valid({ docker_mode: 'dind', daemon_cpu_share: '70', daemon_memory_share: '35' }),
  );
  assert.equal(sent.resources?.daemon_cpu_share_percent, 70);
  assert.equal(sent.resources?.daemon_memory_share_percent, 35);
  const cpuOnly = toPoolBody(valid({ docker_mode: 'dind', daemon_cpu_share: '70' }));
  assert.equal(cpuOnly.resources?.daemon_cpu_share_percent, 70);
  assert.equal(cpuOnly.resources?.daemon_memory_share_percent, undefined);
  for (const over of [
    { docker_mode: 'none' as const, daemon_cpu_share: '70', daemon_memory_share: '35' },
    {
      docker_mode: 'dind' as const,
      daemon_cpu_share: '70',
      daemon_memory_share: '35',
      sizing: 'fixed' as const,
      cpus: '2',
    },
    {
      docker_mode: 'dind' as const,
      daemon_cpu_share: '70',
      daemon_memory_share: '35',
      backend: 'podman' as const,
    },
    { docker_mode: 'dind' as const, daemon_cpu_share: '', daemon_memory_share: '' },
    { docker_mode: 'dind' as const, daemon_cpu_share: '50', daemon_memory_share: '50' },
  ]) {
    const body = toPoolBody(valid(over));
    assert.equal(body.resources?.daemon_cpu_share_percent, undefined);
    assert.equal(body.resources?.daemon_memory_share_percent, undefined);
    assert.equal(body.resources?.daemon_share_percent, undefined);
  }
});

// A PATCH reads an absent key as "leave it alone", so on an edit an emptied
// field has to be sent empty -- otherwise clearing the image is a change the
// server never hears about while the toast says it was saved.
test('an edit sends the optional fields empty, and a create leaves them out', () => {
  const create = toPoolBody(valid());
  for (const key of ['runner_group', 'image', 'runner_version', 'host_selector', 'env'] as const) {
    assert.equal(key in create, false, `${key} is left out on create`);
  }
  const edit = toPoolBody(valid(), { complete: true });
  assert.equal(edit.runner_group, '');
  assert.equal(edit.image, '');
  assert.equal(edit.runner_version, '');
  assert.deepEqual(edit.host_selector, {});
  assert.deepEqual(edit.env, {});
});

test('every timing override is sent, as a duration or as null', () => {
  const body = toPoolBody(valid({ provision_timeout: ' 45m ' }));
  assert.deepEqual(body.runner_settings, {
    provision_timeout: '45m',
    drain_timeout: null,
    max_runner_lifetime: null,
    scale_up_delay: null,
    docker_wait: null,
  });
});

// Only a container runner has a folder to mount over, and only a Docker pool
// with a sidecar has an image store: the server refuses anything else, so the
// draft must not send it however the toggles were left.
test('in-memory folders are sent only where the backend can have them', () => {
  const toggles = {
    tmpfs_work: true,
    tmpfs_tmp: true,
    tmpfs_daemon: true,
    tmpfs_work_size: '2048',
  };
  const process = toPoolBody(valid({ ...toggles, backend: 'process' }));
  assert.equal(process.tmpfs?.work?.enabled, false);
  assert.equal(process.tmpfs?.tmp?.enabled, false);
  assert.equal(process.tmpfs?.daemon?.enabled, false);

  const plain = toPoolBody(valid({ ...toggles, backend: 'docker', docker_mode: 'none' }));
  assert.equal(plain.tmpfs?.work?.enabled, true);
  assert.equal(plain.tmpfs?.work?.size_mb, 2048);
  assert.equal(plain.tmpfs?.daemon?.enabled, false);

  const dind = toPoolBody(valid({ ...toggles, backend: 'docker', docker_mode: 'dind' }));
  assert.equal(dind.tmpfs?.daemon?.enabled, true);
  assert.equal(dind.tmpfs?.work?.auto, true, 'Auto is what a pool starts with');
});

// GitHub adds the default labels to every ephemeral runner itself, so the
// setting only means something on a pool that reuses its runners.
test('leaving out the default labels survives only on a pool that reuses its runners', () => {
  assert.equal(
    toPoolBody(valid({ ephemeral: true, no_default_labels: true })).no_default_labels,
    false,
  );
  assert.equal(
    toPoolBody(valid({ ephemeral: false, no_default_labels: true })).no_default_labels,
    true,
  );
});

// In GitHub's runner settings the prefix is the only thing telling these
// runners from anyone else's, so it is not something a name can lose on the
// way to the server.
test('the name is branded on its way out', () => {
  assert.equal(toPoolBody(valid({ name: 'gpu' })).name, 'zoomies-gpu');
  assert.equal(toPoolBody(valid({ name: 'zoomies-gpu' })).name, 'zoomies-gpu');
});

test('a draft made from a pool and sent back says what the pool said', () => {
  const pool: Pool = {
    id: 'pool_1',
    name: 'zoomies-big',
    installation_id: 'ins_1',
    labels: ['zoomies-big', 'gpu'],
    backend: 'docker',
    docker_mode: 'dind',
    sizing: 'fixed',
    resources: { cpus: 8, memory_mb: 16384, min_memory_mb: 8192, disk_gb: 100 },
    min_runners: 1,
    max_runners: 6,
    priority: 2,
    idle_timeout: '10m',
    ephemeral: true,
    host_selector: { arch: 'amd64' },
    runner_settings: { provision_timeout: '45m' },
    cache: { enabled: true, scope: 'pool', source: '/var/cache/zoomies' },
    cpu_burst: { mode: 'off' },
    memory_burst: { mode: 'automatic', max_memory_mb: 24576, spill_mb: 2048 },
  } as Pool;
  const body = toPoolBody(draftFromPool(pool), { complete: true });
  assert.equal(body.name, 'zoomies-big');
  assert.deepEqual(body.labels, ['zoomies-big', 'gpu']);
  assert.equal(body.resources?.cpus, 8);
  assert.equal(body.resources?.memory_mb, 16384);
  assert.equal(body.resources?.min_memory_mb, 8192);
  assert.equal(body.resources?.disk_gb, 100);
  assert.equal(body.docker_mode, 'dind');
  assert.equal(body.max_runners, 6);
  assert.equal(body.priority, 2);
  assert.deepEqual(body.host_selector, { arch: 'amd64' });
  assert.equal(body.runner_settings?.provision_timeout, '45m');
  assert.equal(body.cache?.enabled, true);
  assert.deepEqual(body.memory_burst, { mode: 'automatic', max_memory_mb: 24576, spill_mb: 2048 });
});

// The valve's figures mean something only while it is on, and the server refuses
// swap on one that is off, so a draft that still holds a ceiling and an allowance
// from when the valve was on must not send them.
test('the memory valve sends its figures only while it is on, and only for a container runner', () => {
  const on = valid({
    memory_burst_mode: 'automatic',
    memory_burst_max: '12288',
    memory_burst_spill: '2048',
  });
  assert.deepEqual(toPoolBody(on).memory_burst, {
    mode: 'automatic',
    max_memory_mb: 12288,
    spill_mb: 2048,
  });
  assert.deepEqual(toPoolBody({ ...on, memory_burst_mode: 'off' }).memory_burst, {
    mode: 'off',
    max_memory_mb: 0,
    spill_mb: 0,
  });
  assert.deepEqual(toPoolBody({ ...on, backend: 'process' }).memory_burst, {
    mode: 'off',
    max_memory_mb: 0,
    spill_mb: 0,
  });
  // A new pool watches, and says nothing else.
  assert.deepEqual(toPoolBody(valid()).memory_burst, {
    mode: 'observe',
    max_memory_mb: 0,
    spill_mb: 0,
  });
});

test('a pool made before the valve existed opens with it off, and sends it off', () => {
  const draft = draftFromPool({ id: 'pool_1', backend: 'docker' } as Pool);
  assert.equal(draft.memory_burst_mode, 'off');
  assert.equal(toPoolBody(draft).memory_burst?.mode, 'off');
});

test('the memory ceiling and the swap are refused where they are typed', () => {
  const on = (over: Partial<PoolDraft>) => valid({ memory_burst_mode: 'automatic', ...over });
  assert.match(
    draftErrors(on({ memory_burst_max: '256' }), false)['memory_burst.max_memory_mb'] ?? '',
    /at least 512 MB/,
  );
  assert.match(
    draftErrors(on({ memory_burst_max: 'lots' }), false)['memory_burst.max_memory_mb'] ?? '',
    /at least 512 MB/,
  );
  assert.equal(
    draftErrors(on({ memory_burst_max: '' }), false)['memory_burst.max_memory_mb'],
    undefined,
    'empty is half as much again',
  );
  // A fixed size is what a runner starts with; a ceiling at or below it has
  // nothing to lend, and a Docker-in-Docker pair starts with the limit twice.
  const fixed = { sizing: 'fixed' as const, cpus: '2', memory_mb: '4096' };
  assert.match(
    draftErrors(on({ ...fixed, memory_burst_max: '4096' }), false)['memory_burst.max_memory_mb'] ??
      '',
    /nothing to lend/,
  );
  assert.equal(
    draftErrors(on({ ...fixed, memory_burst_max: '6144' }), false)['memory_burst.max_memory_mb'],
    undefined,
  );
  assert.match(
    draftErrors(on({ ...fixed, docker_mode: 'dind', memory_burst_max: '8192' }), false)[
      'memory_burst.max_memory_mb'
    ] ?? '',
    /8 GB/,
  );
  assert.ok(draftErrors(on({ memory_burst_spill: '-1' }), false)['memory_burst.spill_mb']);
  assert.match(
    draftErrors(on({ memory_burst_spill: '2000000' }), false)['memory_burst.spill_mb'] ?? '',
    /terabyte/,
  );
  assert.equal(
    draftErrors(on({ memory_burst_spill: '2048' }), false)['memory_burst.spill_mb'],
    undefined,
  );
  // With the valve off, or on a runner that has none, the figures are not checked
  // because they are not sent.
  assert.equal(
    draftErrors(valid({ memory_burst_mode: 'off', memory_burst_spill: '-1' }), false)[
      'memory_burst.spill_mb'
    ],
    undefined,
  );
});

// The new editor opens every section closed, so what flags a pool as having
// been tuned is now only a hint -- but it is still how the first screen decides
// not to claim that a customised pool is on its defaults.
test('a pool is tuned once anything the defaults would not have chosen is set', () => {
  assert.equal(poolIsTuned(emptyDraft()), false);
  for (const over of [
    { sizing: 'fixed' as const },
    { sizing: 'profile' as const },
    { cpu_burst_mode: 'automatic' as const },
    { memory_burst_mode: 'automatic' as const },
    { restrict_hosts: true },
    { host_selector: { arch: 'arm64' } },
    { backend: 'podman' as const },
    { docker_mode: 'host-socket' as const },
    { run_as_root: true },
    { cache_enabled: true },
    { tmpfs_work: true },
    { image: 'registry.example/runner:1' },
    { platform_os: 'debian' },
    { provision_timeout: '45m' },
  ]) {
    assert.equal(poolIsTuned({ ...emptyDraft(), ...over }), true, JSON.stringify(over));
  }
});

test('an empty draft is missing exactly the three things a pool cannot do without', () => {
  const errors = draftErrors(emptyDraft(), false);
  assert.deepEqual(Object.keys(errors).sort(), ['installation_id', 'labels', 'name']);
  assert.deepEqual(draftErrors(valid(), false), {});
});

test('a name has to be the shape the server accepts', () => {
  for (const name of ['-leading', 'has space', 'x'.repeat(65), 'semi;colon']) {
    assert.ok(draftErrors(valid({ name }), false)['name'], name);
  }
  for (const name of ['a', 'zoomies-x64', 'a.b_c-d', '9lives']) {
    assert.equal(draftErrors(valid({ name }), false)['name'], undefined, name);
  }
});

test('the maximum may not be below the minimum, and says what the minimum is', () => {
  const errors = draftErrors(valid({ min_runners: '3', max_runners: '2' }), false);
  assert.match(errors['max_runners'] ?? '', /at least the minimum, which is 3/);
  assert.equal(
    draftErrors(valid({ max_runners: '0' }), false)['max_runners'],
    'Use a whole number, one or more.',
  );
});

// The floors are the server's own: below them the runner binary cannot keep up
// with its own job, or is killed before it takes one. An automatic pool sends no
// figure, so a slider nothing is going to read must not refuse the pool.
test('a fixed size is held to the floors, and an automatic one is not', () => {
  const fixed = draftErrors(valid({ sizing: 'fixed', cpus: '0.1', memory_mb: '256' }), false);
  assert.ok(fixed['resources.cpus']);
  assert.ok(fixed['resources.memory_mb']);
  const empty = draftErrors(valid({ sizing: 'fixed', cpus: '', memory_mb: '' }), false);
  assert.ok(empty['resources.cpus']);
  const automatic = draftErrors(
    valid({ sizing: 'automatic', cpus: '0.1', memory_mb: '256' }),
    false,
  );
  assert.equal(automatic['resources.cpus'], undefined);
  assert.equal(automatic['resources.memory_mb'], undefined);
});

test('a minimum sits at or under the standard and above the floor', () => {
  const over = draftErrors(
    valid({ sizing: 'fixed', cpus: '2', memory_mb: '4096', min_cpus: '4', min_memory_mb: '8192' }),
    false,
  );
  assert.match(over['resources.min_cpus'] ?? '', /at or below the standard CPU/);
  assert.match(over['resources.min_memory_mb'] ?? '', /at or below the standard memory/);
  // Under an automatic size the floor still applies, because it is sent.
  const tiny = draftErrors(valid({ min_cpus: '0.1', min_memory_mb: '128' }), false);
  assert.match(tiny['resources.min_cpus'] ?? '', /quarter of a core/);
  assert.match(tiny['resources.min_memory_mb'] ?? '', /512 MB/);
});

test('each sidecar share stays between ten and ninety percent, and says which resource it is about', () => {
  for (const [key, field, what] of [
    ['daemon_cpu_share', 'resources.daemon_cpu_share_percent', 'CPU'],
    ['daemon_memory_share', 'resources.daemon_memory_share_percent', 'memory'],
  ] as const) {
    for (const share of ['5', '95']) {
      assert.match(
        draftErrors(valid({ [key]: share }), false)[field] ?? '',
        new RegExp(`between 10 and 90 percent of the ${what}`),
        `${key} ${share}`,
      );
    }
    for (const share of ['70', '']) {
      assert.equal(
        draftErrors(valid({ [key]: share }), false)[field],
        undefined,
        `${key} ${share}`,
      );
    }
  }
});

// A pool saved with one figure meant it for both resources, so opening it shows
// that, and a specific figure beats the general one. A pool being edited has its
// division already, which is why it is marked chosen: the recommended preset is
// only ever the start of a new pool.
test('a pool saved with one share opens with it for both, and is never preselected over', () => {
  const general = draftFromPool({ resources: { daemon_share_percent: 70 } } as Pool);
  assert.equal(general.daemon_cpu_share, '70');
  assert.equal(general.daemon_memory_share, '70');
  assert.equal(general.split_chosen, true);
  const specific = draftFromPool({
    resources: { daemon_share_percent: 70, daemon_memory_share_percent: 35 },
  } as Pool);
  assert.equal(specific.daemon_cpu_share, '70');
  assert.equal(specific.daemon_memory_share, '35');
  assert.equal(emptyDraft().split_chosen, false);
});

// A size limit is kept by evicting from a directory on the host: there is
// nothing to measure inside a named volume.
test('a cache size limit needs an absolute host path', () => {
  const named = valid({
    cache_enabled: true,
    cache_size_limit: '1073741824',
    cache_source: 'volume-prefix',
  });
  assert.ok(draftErrors(named, false)['cache.size_limit']);
  const path = { ...named, cache_source: '/var/lib/zoomies/cache' };
  assert.equal(draftErrors(path, false)['cache.size_limit'], undefined);
});

// A tmpfs is charged to the runner's memory limit, so typed sizes that take the
// whole of a typed limit leave a job nothing to run in.
test('in-memory folders that take the whole limit are refused where they are typed', () => {
  const tight = valid({
    sizing: 'fixed',
    cpus: '2',
    memory_mb: '2048',
    tmpfs_work: true,
    tmpfs_work_size: '2048',
  });
  assert.match(
    draftErrors(tight, false)['tmpfs.work.size_mb'] ?? '',
    /raise it to at least 4096 MB/,
  );
  const tiny = valid({ tmpfs_tmp: true, tmpfs_tmp_size: '8' });
  assert.match(draftErrors(tiny, false)['tmpfs.tmp.size_mb'] ?? '', /at least 64/);
});

// Consent is per decision, and the editor resets it when the answer changes.
test('the host socket cannot be saved without the confirmation', () => {
  const socket = valid({ docker_mode: 'host-socket' });
  assert.ok(draftErrors(socket, false)['docker_mode']);
  assert.equal(draftErrors(socket, true)['docker_mode'], undefined);
});

// A pool whose backend no host offers never makes a runner and looks perfectly
// healthy doing it, so the draft refuses it while the fleet has something else.
test('a backend nothing offers is refused while another is on offer', () => {
  const offers: BackendOffer[] = [
    { kind: 'docker', hosts: 2, dindHosts: 2 },
    { kind: 'podman', hosts: 0, dindHosts: 0 },
    { kind: 'process', hosts: 0, dindHosts: 0 },
  ];
  assert.match(
    draftErrors(valid({ backend: 'podman' }), false, offers, true)['backend'] ?? '',
    /Docker \(2 hosts\)/,
  );
  assert.equal(
    draftErrors(valid({ backend: 'docker' }), false, offers, true)['backend'],
    undefined,
  );
  // Until the fleet is known, nothing is claimed about it.
  assert.equal(
    draftErrors(valid({ backend: 'podman' }), false, offers, false)['backend'],
    undefined,
  );
});
