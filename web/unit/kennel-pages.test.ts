import { test } from 'node:test';
import assert from 'node:assert/strict';
import { KENNEL_GROUPS, KENNEL_PAGES } from '../src/lib/kennel/pages.ts';

test('every page has its own id and its own address, and all of them are under Kennel Club', () => {
  assert.equal(new Set(KENNEL_PAGES.map((p) => p.id)).size, KENNEL_PAGES.length);
  assert.equal(new Set(KENNEL_PAGES.map((p) => p.path)).size, KENNEL_PAGES.length);
  for (const page of KENNEL_PAGES) {
    assert.ok(page.path === '/kennel' || page.path.startsWith('/kennel/'), page.path);
    assert.ok(page.label.length > 0);
  }
});

test('the overview is first, and sits at the section’s own address', () => {
  assert.deepEqual([KENNEL_PAGES[0]?.id, KENNEL_PAGES[0]?.path], ['overview', '/kennel']);
});

// The switch turns off what Kennel Club checks. AI Context keeps working with it
// off, so a row for it under the switch would say the opposite of what is true.
test('the switch heads the group it governs, and AI Context is not in that group', () => {
  const governed = KENNEL_GROUPS.filter((g) => g.switch);
  assert.equal(governed.length, 1);
  assert.equal(governed[0]?.id, 'standards');
  assert.deepEqual(
    governed[0]?.pages.map((p) => p.id),
    ['overview', 'repositories'],
  );
  const aiContext = KENNEL_GROUPS.find((g) => g.pages.some((p) => p.id === 'ai-context'));
  assert.ok(aiContext, 'AI Context is listed');
  assert.equal(aiContext.switch, false);
  assert.notEqual(aiContext.id, governed[0]?.id);
});

test('AI Context has a row of its own in the rail', () => {
  assert.equal(KENNEL_PAGES.filter((p) => p.id === 'ai-context').length, 1);
  assert.equal(KENNEL_PAGES.find((p) => p.id === 'ai-context')?.path, '/kennel/ai-context');
});
