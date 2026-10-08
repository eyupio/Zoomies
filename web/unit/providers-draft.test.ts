import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  applyDiscovery,
  emptyDraft,
  draftErrors,
  toProviderBody,
  normaliseEndpoint,
  providerCommand,
  shellWord,
  suggestName,
} from '../src/lib/providers/draft.ts';
import type { ProviderDiscovery, ProviderSetting } from '../src/lib/api/types.ts';

const specs: ProviderSetting[] = [
  {
    key: 'nodes',
    label: 'Nodes',
    kind: 'list',
    required: true,
    advanced: false,
    discovers: 'nodes',
  },
  {
    key: 'template_id',
    label: 'Template VMID',
    kind: 'choice',
    required: true,
    advanced: false,
    discovers: 'templates',
  },
  {
    key: 'bridge',
    label: 'Network bridge',
    kind: 'choice',
    required: true,
    advanced: false,
    discovers: 'bridges',
    default: 'vmbr0',
  },
  {
    key: 'storage',
    label: 'Storage',
    kind: 'choice',
    required: true,
    advanced: false,
    discovers: 'storages',
  },
];

const fullSpecs: ProviderSetting[] = [
  ...specs,
  {
    key: 'vmid_min',
    label: 'Lowest VMID',
    kind: 'number',
    required: true,
    advanced: false,
    default: '9000',
  },
  {
    key: 'vmid_max',
    label: 'Highest VMID',
    kind: 'number',
    required: true,
    advanced: false,
    default: '9099',
  },
  {
    key: 'full_clone',
    label: 'Full clone',
    kind: 'bool',
    required: false,
    advanced: true,
    default: 'true',
  },
  {
    key: 'ipconfig0',
    label: 'IP',
    kind: 'text',
    required: false,
    advanced: true,
    default: 'ip=dhcp',
  },
  { key: 'pool', label: 'Pool', kind: 'text', required: false, advanced: true },
];

function proxmoxDraft() {
  const draft = emptyDraft();
  draft.kind = 'proxmox';
  draft.name = 'proxmox-lab';
  draft.endpoint = 'https://pve.example.com:8006';
  draft.credential = 'zoomies@pve!ci=secret';
  draft.settings = {
    nodes: 'pve1,pve2',
    template_id: '9000',
    storage: 'local-lvm',
    bridge: 'vmbr0',
    vmid_min: '9000',
    vmid_max: '9099',
    pool: 'ci pool',
  };
  draft.machine_labels = { arch: 'amd64' };
  draft.max_machines = '4';
  return draft;
}

// The line the form shows has to be the line the CLI accepts, flag for flag:
// the driver's questions under the flags the CLI spells them as, everything
// else through --setting, and never the credential, which the CLI asks for
// on the terminal so that it does not land in a shell history.
test('the form renders itself as the providers add line the CLI takes', () => {
  const line = providerCommand(proxmoxDraft());
  assert.match(line, /^zoomies providers add proxmox \\\n {2}--name proxmox-lab/);
  for (const want of [
    '--endpoint https://pve.example.com:8006',
    '--nodes pve1,pve2',
    '--template 9000',
    '--storage local-lvm',
    '--bridge vmbr0',
    '--vmid-range 9000-9099',
    "--setting 'pool=ci pool'",
    '--labels arch=amd64',
    '--max-machines 4',
  ]) {
    assert.ok(line.includes(want), `${want} is missing from:\n${line}`);
  }
  assert.ok(!line.includes('secret'), `the credential leaked into the line:\n${line}`);
  assert.ok(!line.includes('--capacity'), 'a default is not repeated as a flag');
});

test('the line is in a fixed order, and leaves out what the driver defaults anyway', () => {
  const draft = proxmoxDraft();
  draft.settings = { ...draft.settings, full_clone: 'true', ipconfig0: 'ip=dhcp' };
  const line = providerCommand(draft, { specs: fullSpecs });
  const order = ['--nodes', '--template', '--storage', '--setting', '--labels'];
  const positions = order.map((flag) => line.indexOf(flag));
  assert.deepEqual(
    [...positions].sort((a, b) => a - b),
    positions,
    line,
  );
  assert.ok(!line.includes('--bridge'), 'vmbr0 is the driver default and the CLI default');
  assert.ok(!line.includes('--vmid-range'), '9000-9099 is the default block');
  assert.ok(!line.includes('full_clone'), 'a default advanced setting is not repeated');
  assert.ok(line.includes("--setting 'pool=ci pool'"), line);
  // An edit sends what was typed, defaults included: it is the whole answer.
  assert.ok(providerCommand(draft, { editing: true, specs: fullSpecs }).includes('--bridge vmbr0'));
});

test('a certificate and a private connection become the placeholders only the operator can fill', () => {
  const draft = proxmoxDraft();
  draft.ca_pem = '-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----';
  draft.connection = 'tailcat';
  draft.tailcat_address = 'tcSECRETADDRESS0000000';
  const line = providerCommand(draft);
  assert.ok(line.includes('--endpoint-ca-file <ca.pem>'), line);
  assert.ok(line.includes('--gateway <address zoomies gateway printed>'), line);
  assert.ok(!line.includes('tcSECRET'), 'the gateway address is sealed and must not be shown');
  assert.ok(!line.includes('BEGIN CERTIFICATE'), 'the certificate is a file on the CLI side');
});

test('an edit names the provider and only what differs from the defaults', () => {
  const draft = proxmoxDraft();
  draft.max_machines = '8';
  const line = providerCommand(draft, { editing: true, existingName: 'proxmox-lab' });
  assert.match(line, /^zoomies providers edit proxmox-lab/);
  assert.ok(line.includes('--max-machines 8'), line);
  assert.ok(!line.includes('--name'), 'an unchanged name is not sent');
});

test('shell words are quoted only when they need to be', () => {
  assert.equal(shellWord('local-lvm'), 'local-lvm');
  assert.equal(shellWord('ci pool'), "'ci pool'");
  assert.equal(shellWord("it's"), "'it'\\''s'");
  assert.equal(shellWord(''), "''");
});

// People type the host; the API wants an origin with a scheme and the port
// the driver listens on. Anything already complete is left alone.
test('an endpoint gains the scheme and the example port it was typed without', () => {
  const example = 'https://pve.example.com:8006';
  assert.equal(normaliseEndpoint('pve.home', example), 'https://pve.home:8006');
  assert.equal(normaliseEndpoint('https://pve.home', example), 'https://pve.home:8006');
  assert.equal(normaliseEndpoint('https://pve.home:8443', example), 'https://pve.home:8443');
  assert.equal(normaliseEndpoint('http://127.0.0.1:8006/', example), 'http://127.0.0.1:8006');
  assert.equal(normaliseEndpoint('  ', example), '');
  assert.equal(normaliseEndpoint('pve.home', undefined), 'https://pve.home');
  assert.equal(normaliseEndpoint('not a url at all', example), 'not a url at all');
});

test('a name is suggested from the host, and the kind stands in when there is no host', () => {
  assert.equal(suggestName('https://pve.example.com:8006', 'proxmox'), 'pve');
  assert.equal(suggestName('https://Lab-Cluster.local:8006', 'proxmox'), 'lab-cluster');
  assert.equal(suggestName('https://10.0.0.5:8006', 'proxmox'), 'proxmox');
  assert.equal(suggestName('', 'proxmox'), 'proxmox');
});

// Discovery fills in what is not a question -- one node, one template -- and
// replaces a published default the cluster does not have when there is one
// thing to replace it with. An answer the operator gave stays theirs.
test('discovery answers the questions with one answer and leaves the rest', () => {
  const draft = emptyDraft();
  draft.settings = { bridge: 'vmbr0', storage: 'local-lvm' };
  const discovery: ProviderDiscovery = {
    nodes: [{ value: 'pve1' }],
    templates: [{ value: '9000', label: 'zoomies-template' }],
    bridges: [{ value: 'vmbr1' }],
    storages: [{ value: 'ceph' }, { value: 'local-lvm' }],
  };
  const next = applyDiscovery(draft, specs, discovery);
  assert.equal(next.settings.nodes, 'pve1', 'the only node is chosen');
  assert.equal(next.settings.template_id, '9000', 'the only template is chosen');
  assert.equal(
    next.settings.bridge,
    'vmbr1',
    'a default the cluster lacks gives way to the one bridge it has',
  );
  assert.equal(next.settings.storage, 'local-lvm', 'a choice the operator made stays');

  const two: ProviderDiscovery = { ...discovery, nodes: [{ value: 'pve1' }, { value: 'pve2' }] };
  const open = applyDiscovery(emptyDraft(), specs, two);
  assert.equal(open.settings.nodes, undefined, 'two nodes is a question, not an answer');
  assert.equal(applyDiscovery(draft, specs, null), draft, 'no discovery changes nothing');
});

test('a ready Proxmox setup supplies the connection without sending secrets', () => {
  const draft = emptyDraft();
  draft.kind = 'proxmox';
  draft.setup_id = 'pvs_ready';
  draft.name = 'proxmox-pve-1';
  draft.endpoint = 'https://pve.example:8006';
  draft.connection = 'tailcat';
  draft.tailcat_configured = true;
  const errors = draftErrors(draft, []);
  assert.equal(errors.credential, undefined);
  assert.equal(errors.tailcat_address, undefined);
  const body = toProviderBody(draft);
  assert.equal(body.setup_id, 'pvs_ready');
  assert.equal(body.credential, undefined);
  assert.equal(body.tailcat_address, undefined);
  assert.equal(body.max_machines, 0);
});
