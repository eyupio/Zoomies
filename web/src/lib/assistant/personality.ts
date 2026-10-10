import { THINKING_MOTIONS } from '../mascot/dog-motion';

export const THINKING_QUOTES = [
  'Give me a sniff. There is an answer here somewhere.',
  'Brain zoomies in progress. Mind the paws.',
  'Fetching a thought. Hopefully not a tennis ball.',
  'One ear on your question. One ear on the biscuit tin.',
  'Chasing the useful bit. It is surprisingly speedy.',
  'Good questions deserve a proper tail-wag.',
  'Paws on keyboard. Very serious business.',
  'Following the scent. Please hold my biscuit.',
  'A small thought has escaped. Pursuing it now.',
  'Consulting my inner good dog.',
  'Putting two and two together. Hoping for four treats.',
  'Just doing a lap around the idea.',
  'Head tilted. Ears engaged. Thinking continues.',
  'Digging for the useful bit. Your flowerbeds are safe.',
  'Fetching the answer with only a little slobber.',
  'There is a thought under this sofa. I can feel it.',
  'My thinking face looks a lot like my biscuit face.',
  'Taking this question for a little walk.',
  'Sit. Stay. Contemplate.',
  'A brief paws for thought.',
  'Nose down, ears up. Working on it.',
  'No squirrels were consulted in the making of this thought.',
  'Untangling the idea. It has brought its own lead.',
  'One more sniff before I bring this back.',
] as const;

export const THINKING_QUOTE_MS = 4500;

/** Two quotes per routine, and every quote gets a turn before one repeats. */
export function thinkingBeat(step: number, offset = 0) {
  return {
    quote: THINKING_QUOTES[(step + offset) % THINKING_QUOTES.length],
    motion: THINKING_MOTIONS[Math.floor((step + offset) / 2) % THINKING_MOTIONS.length],
  };
}
