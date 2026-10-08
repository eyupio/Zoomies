import { test } from 'node:test';
import assert from 'node:assert/strict';
import { KENNEL_GROUPS, KENNEL_PAGES, kennelListHref } from '../src/lib/kennel/pages.ts';

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

// The list reads its filters from the address, so the link is the whole of "show
// me these". An empty filter in the address would read as a filter that matches
// nothing to anyone who copied it.
test('the list of repositories is opened on the address the list itself reads', () => {
  const list = KENNEL_PAGES.find((p) => p.id === 'repositories')?.path;
  assert.equal(kennelListHref(), list);
  assert.equal(kennelListHref({}), list);
  assert.equal(kennelListHref({ state: 'attention' }), `${list}?state=attention`);
  assert.equal(kennelListHref({ severity: 'error' }), `${list}?severity=error`);
  assert.equal(
    kennelListHref({ state: 'attention', severity: 'warning' }),
    `${list}?state=attention&severity=warning`,
  );
});

// The list leaves out the repositories the fleet is not serving unless told. A link
// that counts every repository has to say so, or it opens on fewer rows than the
// number it came from.
test('a link that counts every repository asks the list for every repository', () => {
  const list = kennelListHref();
  assert.equal(kennelListHref({ everything: true }), `${list}?active=all`);
  assert.equal(
    kennelListHref({ state: 'attention', everything: true }),
    `${list}?state=attention&active=all`,
  );
  assert.equal(
    kennelListHref({ severity: 'error', everything: true }),
    `${list}?severity=error&active=all`,
  );
  assert.equal(kennelListHref({ everything: false }), list, 'not asked for, not in the address');
});

// The list leaves out what Kennel Club was told not to look at, because the cards count
// what it is looking at. The card for the others has to ask for them by name.
test('the card for the repositories nobody tracks asks the list for them', () => {
  const list = kennelListHref();
  assert.equal(kennelListHref({ notTracked: true }), `${list}?tracked=false`);
  assert.equal(
    kennelListHref({ notTracked: true, everything: true }),
    `${list}?tracked=false&active=all`,
  );
  assert.equal(kennelListHref({ notTracked: false }), list, 'not asked for, not in the address');
});

test('an empty filter is left out of the address, and a value is encoded', () => {
  const list = kennelListHref();
  assert.equal(kennelListHref({ state: '', severity: '' }), list);
  assert.equal(kennelListHref({ state: '', severity: 'error' }), `${list}?severity=error`);
  assert.equal(kennelListHref({ state: 'a&b' }), `${list}?state=a%26b`);
});
