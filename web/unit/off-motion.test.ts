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
  TrendingDown,
  TrendingUp,
  Zap,
} from '@lucide/svelte';
import { offMotion } from '../src/lib/components/off-motion';

test('only work still in progress moves, and each glyph moves its own way', () => {
  // Six glyphs, six motions: being set up turns, work executing pulses along
  // its trace, being accepted breathes, finishing turns its hourglass over,
  // extra CPU lifts its trend line and maximum CPU charges its bolt.
  const moving = [LoaderCircle, Activity, Handshake, Hourglass, TrendingUp, Zap].map(offMotion);
  assert.deepEqual(moving, ['spin', 'trace', 'breathe', 'turn', 'lift', 'charge']);
});

test('whatever is waiting or finished holds still', () => {
  // A failure that moved would draw the eye the way a healthy runner should
  // not, a page of idle runners should be a calm one, and CPU being held back
  // is not work going on.
  for (const icon of [
    TrendingDown,
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
