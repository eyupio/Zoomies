import { test } from 'node:test';
import assert from 'node:assert/strict';
import { contextPrompt, followUps } from '../src/lib/assistant/prompts';

test('context keeps measured zero and false, omits absent facts, and names the snapshot', () => {
  const prompt = contextPrompt({
    kind: 'host',
    title: 'build-1',
    facts: { Free: 0, Healthy: false, CPU: undefined },
  });
  assert.match(prompt, /Free: 0/);
  assert.match(prompt, /Healthy: false/);
  assert.doesNotMatch(prompt, /CPU:/);
  assert.match(prompt, /snapshot of displayed facts, not live access/);
});

test('follow-ups prioritise the question over incidental resource names in its context', () => {
  const turns = [
    { role: 'user', content: 'Help me diagnose this runner failure.\nHost: build-1' },
    { role: 'assistant', content: 'Check the host and runner logs.' },
  ];
  const suggestions = followUps(turns);
  assert.equal(suggestions[0]?.label, 'Trace the failure');
  const prompt = suggestions[0]!.prompt;
  const next = followUps([
    ...turns,
    { role: 'user', content: prompt },
    { role: 'assistant', content: 'Collect container logs.' },
  ]);
  assert.ok(next.length > 0);
  assert.ok(next.every((item) => item.prompt !== prompt));
});

test('streaming, failed and empty answers do not suggest a next step', () => {
  for (const answer of [
    { content: '', streaming: true },
    { content: 'partial', error: 'offline' },
    { content: '' },
  ]) {
    assert.deepEqual(
      followUps([
        { role: 'user', content: 'Host overloaded' },
        { role: 'assistant', ...answer },
      ]),
      [],
    );
  }
});

test('a long investigation still offers ways to continue after the initial suggestions', () => {
  const turns: { role: string; content: string }[] = [
    { role: 'user', content: 'host overload' },
    { role: 'assistant', content: 'Check host pressure.' },
  ];
  for (let i = 0; i < 9; i++) {
    const suggestion = followUps(turns)[0];
    assert.ok(suggestion);
    turns.push(
      { role: 'user', content: suggestion.prompt },
      { role: 'assistant', content: 'Check host pressure with the new evidence.' },
    );
  }
});
