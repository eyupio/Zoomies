import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  FEED_CATEGORIES,
  feedIsFiltered,
  type FeedCategory,
  type FeedCategoryID,
} from '../src/lib/feed/categories.ts';
import { emptyFeedCopy } from '../src/lib/feed/empty.ts';

/** A browser's choices, as `prefs.feedChoice` hands them back: absent means undecided. */
function choices(
  made: Partial<Record<FeedCategoryID, boolean | null>>,
): (id: FeedCategoryID) => boolean | null | undefined {
  return (id) => made[id];
}

// The bug this guards: two kinds start off, so a browser nobody has touched
// always has kinds switched off, and an empty feed there used to say "2 kinds of
// event are switched off for this browser" -- blaming a choice nobody made, and
// putting the first-run copy that was written for exactly that moment out of
// reach.
test('a browser nobody has touched is not filtering, although some kinds start off', () => {
  // Without this the test proves nothing: it is only the bug while the
  // catalogue has kinds that begin off.
  assert.ok(
    FEED_CATEGORIES.some((category) => !category.on),
    'the catalogue has kinds that start off',
  );
  assert.equal(
    feedIsFiltered(() => undefined),
    false,
  );
  // A preference that has been through JSON can hand back null for "no choice".
  assert.equal(
    feedIsFiltered(() => null),
    false,
  );
});

test('a choice that agrees with the default changes nothing', () => {
  const agreeing = Object.fromEntries(
    FEED_CATEGORIES.map((category) => [category.id, category.on]),
  );
  assert.equal(feedIsFiltered(choices(agreeing)), false);
});

// The operator who reads "nothing in the kinds you are watching" has to have
// been the one to narrow them.
test('switching off a kind that starts on is a filter', () => {
  const started = FEED_CATEGORIES.find((category) => category.on);
  assert.ok(started);
  assert.equal(feedIsFiltered(choices({ [started.id]: false })), true);
});

// "N kinds are switched off" is still true when the operator widened the feed
// and left a default-off kind off, and they have plainly been to the settings
// page, so the sentence is theirs to be told.
test('turning on one kind that starts off, while another still is, counts as the operator’s change', () => {
  const offByDefault = FEED_CATEGORIES.filter((category) => !category.on);
  assert.ok(offByDefault.length >= 2, 'needs two kinds that start off for this to be the case');
  const [first] = offByDefault;
  assert.ok(first);
  assert.equal(feedIsFiltered(choices({ [first.id]: true })), true);
});

// An operator who has switched on everything has nothing off to be told about:
// "0 kinds are switched off" is not a sentence.
test('with every kind on there is nothing switched off to blame anyone for', () => {
  const everything = Object.fromEntries(FEED_CATEGORIES.map((category) => [category.id, true]));
  assert.equal(feedIsFiltered(choices(everything)), false);
});

// The function answers for the catalogue it is given, which is what lets it be
// checked without the real list's icons and defaults moving under it.
test('it reads the catalogue it is handed rather than assuming the real one', () => {
  const small = [
    { id: 'scaling', on: true },
    { id: 'audit', on: false },
  ] as unknown as readonly FeedCategory[];
  assert.equal(
    feedIsFiltered(() => undefined, small),
    false,
  );
  assert.equal(feedIsFiltered(choices({ scaling: false }), small), true);
  assert.equal(feedIsFiltered(choices({ audit: false }), small), false);
  assert.equal(feedIsFiltered(choices({ audit: true }), small), false, 'nothing is left off');
});

// The two reasons for an empty feed need different words, and the first-run
// ones were unreachable on a default configuration because the count of kinds
// that are off -- which is never zero -- was what chose between them.
test('an empty feed nobody has filtered says nothing has happened, and what will', () => {
  const noFleet = emptyFeedCopy({ filtered: false, hidden: 2, hasFleet: false });
  assert.equal(noFleet.title, 'Nothing has happened yet');
  assert.match(noFleet.description, /Once there is a pool and a host/);

  const quiet = emptyFeedCopy({ filtered: false, hidden: 2, hasFleet: true });
  assert.equal(quiet.title, 'Nothing has happened yet');
  assert.match(quiet.description, /A line is written here every time a job finishes/);

  // Neither blames the browser, however many kinds happen to be off.
  for (const copy of [noFleet, quiet]) {
    assert.doesNotMatch(`${copy.title} ${copy.description}`, /switched off|you are watching/);
  }
});

test('an empty feed the operator has filtered says so, and counts the kinds out loud', () => {
  const one = emptyFeedCopy({ filtered: true, hidden: 1, hasFleet: true });
  assert.equal(one.title, 'Nothing in the kinds you are watching');
  assert.match(one.description, /^1 kind of event is switched off for this browser\./);

  const several = emptyFeedCopy({ filtered: true, hidden: 3, hasFleet: false });
  assert.match(several.description, /^3 kinds of event are switched off for this browser\./);
  assert.match(several.description, /Choose above turns them back on\.$/);
});

// The two halves together, as the panel uses them: a browser that has not been
// touched is told the first-run words although two kinds are off, and the same
// browser after the operator switches one more off is told the other.
test('a default browser gets the first-run words and a filtering one gets the filter words', () => {
  const hidden = FEED_CATEGORIES.filter((category) => !category.on).length;
  assert.ok(hidden > 0);

  const untouched = feedIsFiltered(() => undefined);
  assert.equal(
    emptyFeedCopy({ filtered: untouched, hidden, hasFleet: false }).title,
    'Nothing has happened yet',
  );

  const started = FEED_CATEGORIES.find((category) => category.on);
  assert.ok(started);
  const narrowed = feedIsFiltered((id) => (id === started.id ? false : undefined));
  assert.equal(
    emptyFeedCopy({ filtered: narrowed, hidden: hidden + 1, hasFleet: false }).title,
    'Nothing in the kinds you are watching',
  );
});
