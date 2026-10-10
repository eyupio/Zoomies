import { test } from 'node:test';
import assert from 'node:assert/strict';
import { contextPrompt, splitFollowUps } from '../src/lib/assistant/prompts';

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

test('the follow-up block is read out of the answer and never shown', () => {
  const { text, suggestions } = splitFollowUps(
    'Check the host.\n\n<follow-ups>\n- Which logs first?\n2. How do I drain it?\nWhich logs first?\n</follow-ups>\n',
  );
  assert.equal(text, 'Check the host.');
  assert.deepEqual(
    suggestions.map((s) => s.prompt),
    ['Which logs first?', 'How do I drain it?'],
  );
});

test('a half-streamed block or tag is withheld, and an unclosed one suggests nothing', () => {
  assert.equal(splitFollowUps('Answer.\n\n<follo').text, 'Answer.');
  assert.equal(splitFollowUps('Use a < b.').text, 'Use a < b.');
  const open = splitFollowUps('Answer.\n<follow-ups>\nWhy is it');
  assert.equal(open.text, 'Answer.');
  assert.deepEqual(open.suggestions, []);
});
