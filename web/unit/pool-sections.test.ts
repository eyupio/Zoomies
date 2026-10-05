import { test } from 'node:test';
import assert from 'node:assert/strict';
import { emptyDraft, draftErrors } from '../src/lib/pools/draft.ts';
import type { PoolDraft } from '../src/lib/pools/draft.ts';
import {
  SECTIONS,
  editedSections,
  sectionDef,
  sectionForField,
} from '../src/lib/pools/sections.ts';
import { summarise } from '../src/lib/pools/summaries.ts';

const draft = (over: Partial<PoolDraft> = {}): PoolDraft => ({
  ...emptyDraft(),
  name: 'zoomies-linux-x64',
  installation_id: 'ins_1',
  labels: ['zoomies-linux-x64'],
  ...over,
});

// A refusal is only useful if it opens the part of the page that can fix it,
// and the server's field names are the only thing that says which that is. A
// field no section lists would be refused with nowhere to go.
test('every field the editor can be refused on belongs to exactly one section', () => {
  const seen = new Map<string, string>();
  for (const section of SECTIONS) {
    for (const field of section.fields) {
      assert.equal(seen.get(field), undefined, `${field} is listed twice`);
      seen.set(field, section.id);
    }
  }
  // The rules the browser checks for itself, whichever field they are filed under.
  const everyRule = draftErrors(
    draft({
      name: '',
      installation_id: '',
      labels: [],
      min_runners: 'x',
      max_runners: '0',
      priority: 'x',
      idle_timeout: 'soon',
      sizing: 'fixed',
      cpus: '',
      memory_mb: '',
      min_cpus: '0.1',
      min_memory_mb: '1',
      daemon_share: '5',
      disk_gb: '-1',
      cpu_burst_max: '0.1',
      cache_enabled: true,
      cache_size_limit: '5',
      cache_source: 'volume',
      tmpfs_work: true,
      tmpfs_work_size: '1',
      tmpfs_tmp: true,
      tmpfs_tmp_size: '1',
      docker_mode: 'host-socket',
    }),
    false,
  );
  for (const field of Object.keys(everyRule)) {
    assert.notEqual(sectionForField(field), null, `${field} has no section to open`);
  }
});

test('a field is pointed at the section that owns it', () => {
  assert.equal(sectionForField('name'), 'basics');
  assert.equal(sectionForField('host_selector'), 'hosts');
  assert.equal(sectionForField('docker_mode'), 'runner');
  assert.equal(sectionForField('resources.memory_mb'), 'size');
  assert.equal(sectionForField('cpu_burst.max_cpus'), 'size');
  assert.equal(sectionForField('runner_settings.provision_timeout'), 'scaling');
  assert.equal(sectionForField('tmpfs.daemon.size_mb'), 'speed');
  assert.equal(sectionForField('cache.size_limit'), 'speed');
  // The environment is carried through untouched, so there is nothing to open.
  assert.equal(sectionForField('env'), null);
});

test('six sections, each with a name and a sentence about what it decides', () => {
  assert.equal(SECTIONS.length, 6);
  assert.deepEqual(
    SECTIONS.map((section) => section.id),
    ['basics', 'hosts', 'runner', 'size', 'scaling', 'speed'],
  );
  for (const section of SECTIONS) {
    assert.ok(section.title.length > 0);
    assert.ok(section.description.endsWith('.'), `${section.id} says what it decides`);
    assert.equal(sectionDef(section.id), section);
  }
});

// "Edited" is the promise that Save will change something there, so it is a
// comparison of what is in the section now with what it started as -- typing a
// figure and typing the old one back is not an edit.
test('a section is edited only while its answers differ from where it started', () => {
  const start = draft();
  assert.equal(editedSections(start, start).size, 0);

  const changed = draft({ max_runners: '9', cache_enabled: true });
  assert.deepEqual([...editedSections(changed, start)].sort(), ['scaling', 'speed']);

  const typedBack = draft({ max_runners: '9' });
  typedBack.max_runners = start.max_runners;
  assert.equal(editedSections(typedBack, start).size, 0);
});

// Docker is asked on the first section and set on the third, so changing it
// from either place marks both: each of them holds the answer.
test('the Docker answer marks both the section that asks it and the one that sets it', () => {
  const edited = editedSections(draft({ docker_mode: 'dind' }), draft());
  assert.deepEqual([...edited].sort(), ['basics', 'runner']);
});

test('the first section says what a workflow writes, who it is for and whether Docker is on', () => {
  const lines = summarise(draft({ labels: ['zoomies-gpu', 'cuda12'], docker_mode: 'dind' }), {
    installation: 'eyupio',
  });
  assert.equal(lines.basics, 'runs-on zoomies-gpu, cuda12 · eyupio · Docker for jobs');
  assert.equal(
    summarise(draft({ labels: [], installation_id: '' })).basics,
    'no labels yet · no GitHub connection chosen',
  );
});

test('hosts say whether a pool may use any machine or only some, and how many that is', () => {
  assert.equal(summarise(draft()).hosts, 'Any host that can run it');
  assert.equal(
    summarise(draft(), { hostsTotal: 3 }).hosts,
    'Any host that can run it · 3 connected',
  );
  const some = draft({ restrict_hosts: true, host_selector: { arch: 'arm64', os: 'linux' } });
  assert.equal(
    summarise(some, { hostsTotal: 3, hostsMatching: 1 }).hosts,
    'Only hosts where arch=arm64, os=linux · 1 match',
  );
  // A rule chosen but not yet written is not claimed to be anything.
  assert.equal(
    summarise(draft({ restrict_hosts: true })).hosts,
    'Only some hosts · no rule chosen yet',
  );
});

test('the runner says what it is made of, and only what was changed from the default', () => {
  assert.equal(summarise(draft()).runner, 'Docker · default image');
  const tuned = draft({
    backend: 'podman',
    platform_os: 'debian',
    platform_os_version: '12',
    image: 'registry.example/runner:1',
    runner_version: '2.330.0',
    run_as_root: true,
  });
  assert.equal(
    summarise(tuned).runner,
    'Podman · Debian 12 · your own image · runner 2.330.0 · runs as root',
  );
  // A process runner has no image to speak of.
  assert.equal(summarise(draft({ backend: 'process' })).runner, 'Process');
});

test('the size says what one runner gets and whether it may borrow CPU', () => {
  assert.equal(
    summarise(draft()).size,
    'One share of each host · elastic CPU observing',
    'a new pool starts on observe',
  );
  assert.equal(
    summarise(draft({ cpu_burst_mode: 'automatic' })).size,
    'One share of each host · elastic CPU boosting',
  );
  assert.equal(
    summarise(draft({ cpu_burst_mode: 'off', sizing: 'profile' })).size,
    'The size each host sets',
  );
  assert.equal(
    summarise(draft({ sizing: 'fixed', cpus: '4', memory_mb: '8192', min_memory_mb: '4096' })).size,
    '4 cores and 8 GB on every host · never below 4 GB',
    'a fixed size cannot borrow, so it says nothing of elastic CPU',
  );
  assert.equal(
    summarise(draft({ backend: 'process', cpu_burst_mode: 'observe' })).size,
    'One share of each host',
    'a process runner has no live quota to borrow against',
  );
});

test('scaling says how many runners, for how long, and only the unusual', () => {
  assert.equal(summarise(draft()).scaling, '0 to 4 runners · idle 5m');
  const unusual = draft({
    min_runners: '1',
    max_runners: '8',
    ephemeral: false,
    priority: '2',
    provision_timeout: '45m',
    drain_timeout: '10m',
  });
  assert.equal(
    summarise(unusual).scaling,
    '1 to 8 runners · idle 5m · reused between jobs · priority 2 · 2 timing overrides',
  );
  assert.equal(
    summarise(draft({ provision_timeout: '45m' })).scaling,
    '0 to 4 runners · idle 5m · 1 timing override',
  );
});

// A pool loaded from the API carries Go's own spelling, "5m0s", and an operator
// reading the row says "5m"; a value half typed is left as it is.
test('the idle timeout is said the way an operator says it', () => {
  assert.equal(summarise(draft({ idle_timeout: '5m0s' })).scaling, '0 to 4 runners · idle 5m');
  assert.equal(summarise(draft({ idle_timeout: '90s' })).scaling, '0 to 4 runners · idle 1m30s');
  assert.equal(summarise(draft({ idle_timeout: '1h0m0s' })).scaling, '0 to 4 runners · idle 1h');
  assert.equal(summarise(draft({ idle_timeout: '5mm' })).scaling, '0 to 4 runners · idle 5mm');
});

test('speed-ups say what is kept in memory and whether there is a cache', () => {
  assert.equal(summarise(draft()).speed, 'Scratch space on disk · no cache');
  assert.equal(
    summarise(draft({ tmpfs_work: true, tmpfs_tmp: true })).speed,
    'work folder, /tmp in memory where there is room · no cache',
  );
  assert.equal(
    summarise(draft({ tmpfs_work: true, tmpfs_auto: false, cache_enabled: true })).speed,
    'work folder in memory · shared cache',
  );
  // The image store is the sidecar's, so a pool without one does not claim it.
  assert.equal(
    summarise(draft({ tmpfs_daemon: true, tmpfs_work: true })).speed,
    'work folder in memory where there is room · no cache',
  );
  assert.equal(
    summarise(
      draft({
        docker_mode: 'dind',
        tmpfs_daemon: true,
        cache_enabled: true,
        cache_scope: 'repository',
        cache_tools: true,
      }),
    ).speed,
    'image store in memory where there is room · per-repository cache with tools',
  );
  // A process runner has nothing to mount over.
  assert.equal(
    summarise(draft({ backend: 'process', tmpfs_work: true })).speed,
    'Scratch space on disk · no cache',
  );
});
