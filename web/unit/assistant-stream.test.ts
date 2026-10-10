import { test } from 'node:test';
import assert from 'node:assert/strict';
import { FrameParser } from '../src/lib/api/assistantStream.ts';

const frame = (kind: string, data: unknown) => `event: ${kind}\ndata: ${JSON.stringify(data)}\n\n`;

test('the frames of an answer are read in order', () => {
  const p = new FrameParser();
  const got = p.push(
    frame('delta', { text: 'Hello' }) +
      frame('delta', { text: ' there' }) +
      frame('usage', { input_tokens: 7, output_tokens: 4 }) +
      frame('done', { provider: 'Ollama', model: 'llama3' }),
  );
  assert.deepEqual(got, [
    { kind: 'delta', text: 'Hello' },
    { kind: 'delta', text: ' there' },
    { kind: 'usage', inputTokens: 7, outputTokens: 4 },
    {
      kind: 'done',
      provider: 'Ollama',
      model: 'llama3',
      fleetAccess: false,
      tools: [],
      redacted: { credentials: 0, emails: 0 },
      cut: false,
      providerId: '',
      outputLimit: 0,
    },
  ]);
});

// A provider that stopped the answer at its output ceiling says so on the done
// frame, so the page can say it rather than show silence as a finished answer.
test('a done frame says when the answer was cut, on which provider, and at what limit', () => {
  const p = new FrameParser();
  const [got] = p.push(
    frame('done', {
      provider: 'Ollama',
      model: 'llama3',
      cut: true,
      provider_id: 'asp_1',
      output_limit: 8192,
    }),
  );
  assert.equal(got?.kind, 'done');
  assert.equal(got?.kind === 'done' && got.cut, true);
  assert.equal(got?.kind === 'done' && got.providerId, 'asp_1');
  assert.equal(got?.kind === 'done' && got.outputLimit, 8192);
});

test('a frame cut anywhere by the network is read once it is whole', () => {
  const whole = frame('delta', { text: 'split' }) + frame('done', { provider: 'p', model: 'm' });
  for (let cut = 1; cut < whole.length; cut++) {
    const p = new FrameParser();
    const got = [...p.push(whole.slice(0, cut)), ...p.push(whole.slice(cut))];
    assert.deepEqual(
      got,
      [
        { kind: 'delta', text: 'split' },
        {
          kind: 'done',
          provider: 'p',
          model: 'm',
          fleetAccess: false,
          tools: [],
          redacted: { credentials: 0, emails: 0 },
          cut: false,
          providerId: '',
          outputLimit: 0,
          providerId: '',
          outputLimit: 0,
        },
      ],
      `cut at ${cut}`,
    );
  }
});

test('carriage returns, heartbeats and frames from a newer controller are ignored', () => {
  const p = new FrameParser();
  const got = p.push(
    ': keep-alive\n\n' +
      'event: delta\r\ndata: {"text":"a"}\r\n\r\n' +
      frame('something_new', { x: 1 }) +
      'event: delta\ndata: not json\n\n' +
      'data: {"text":"no kind"}\n\n' +
      frame('error', { message: 'it failed' }),
  );
  assert.deepEqual(got, [
    { kind: 'delta', text: 'a' },
    { kind: 'error', message: 'it failed' },
  ]);
});

test('text with newlines and characters beyond one byte survives', () => {
  const p = new FrameParser();
  const text = 'line one\nline two é 日本';
  assert.deepEqual(p.push(frame('delta', { text })), [{ kind: 'delta', text }]);
});

test('a frame that never ends is held and not guessed at', () => {
  const p = new FrameParser();
  assert.deepEqual(p.push('event: delta\ndata: {"text":"a"}\n'), []);
});

test('a look at the fleet is a frame, and the end says which tools were used', () => {
  const got = new FrameParser().push(
    frame('tool', { name: 'fleet_status', status: 'running' }) +
      frame('tool', { name: 'fleet_status', status: 'done' }) +
      frame('done', {
        provider: 'p',
        model: 'm',
        fleet_access: true,
        tools: ['fleet_status', 7, 'list_jobs'],
      }),
  );
  assert.deepEqual(got, [
    { kind: 'tool', name: 'fleet_status', status: 'running' },
    { kind: 'tool', name: 'fleet_status', status: 'done' },
    {
      kind: 'done',
      provider: 'p',
      model: 'm',
      fleetAccess: true,
      tools: ['fleet_status', 'list_jobs'],
      redacted: { credentials: 0, emails: 0 },
      cut: false,
      providerId: '',
      outputLimit: 0,
    },
  ]);
});

test('a tool frame with a status this page does not know is left unsaid', () => {
  assert.deepEqual(new FrameParser().push(frame('tool', { name: 'x', status: 'exploded' })), []);
});

test('the end says how many credentials and email addresses were hidden, and nothing odd is believed', () => {
  const done = (redacted: unknown) =>
    new FrameParser().push(frame('done', { provider: 'p', model: 'm', redacted }))[0];
  assert.deepEqual(done({ credentials: 2, emails: 1 }), {
    kind: 'done',
    provider: 'p',
    model: 'm',
    fleetAccess: false,
    tools: [],
    redacted: { credentials: 2, emails: 1 },
    cut: false,
    providerId: '',
    outputLimit: 0,
  });
  for (const odd of [
    undefined,
    null,
    'many',
    [],
    { credentials: -1, emails: 1.5 },
    { credentials: '2' },
  ]) {
    const got = done(odd);
    assert.ok(got && got.kind === 'done');
    assert.deepEqual(got.redacted, { credentials: 0, emails: 0 });
  }
});
