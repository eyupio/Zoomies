import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  Activity,
  ChevronsUp,
  CircleDot,
  CircleMinus,
  CircleSlash,
  CircleX,
  Clock,
  Handshake,
  Hourglass,
  LoaderCircle,
  OctagonPause,
  TimerOff,
} from '@lucide/svelte';
import { offMotion } from '../src/lib/components/off-motion';

test('only work still in progress moves, and each glyph moves its own way', () => {
  // Four glyphs, four motions: being set up turns, work executing pulses along
  // its trace, being accepted breathes and finishing turns its hourglass over.
  const moving = [LoaderCircle, Activity, Handshake, Hourglass].map(offMotion);
  assert.deepEqual(moving, ['spin', 'trace', 'breathe', 'turn']);
});

test('whatever is waiting or finished holds still', () => {
  // A failure that moved would draw the eye the way a healthy runner should
  // not, and a page of idle runners should be a calm one.
  for (const icon of [
    CircleDot,
    Clock,
    ChevronsUp,
    OctagonPause,
    CircleSlash,
    TimerOff,
    CircleX,
    CircleMinus,
  ])
    assert.equal(offMotion(icon), 'none');
});
