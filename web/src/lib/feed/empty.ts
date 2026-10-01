/**
 * What the Overview's feed says when it has nothing to show.
 *
 * There are two different reasons for an empty feed and they are not told apart
 * by looking at the feed: a fleet that has not done anything yet, and a browser
 * that has switched off every kind of event the fleet has been doing. They need
 * different words -- the first says what will appear and when, the second says
 * where to turn the kinds back on -- and getting them the wrong way round
 * tells a new operator they did something they did not, which is how the
 * first-run copy came to be unreachable on a default configuration.
 *
 * The words are here rather than in the panel so that every one of them can be
 * checked without drawing it.
 */
import { pluralise } from '../format';

export interface EmptyFeedCopy {
  title: string;
  description: string;
}

/**
 * `filtered` is whether the operator has narrowed the feed themselves --
 * `feedIsFiltered`, not merely "some kinds are off", since two start off.
 * `hidden` is how many kinds are off, which the filtered words count out loud.
 * `hasFleet` is whether there is a pool or a host to have happened to: what a
 * quiet fleet will write is worth saying, and what a fleet that does not exist
 * yet is waiting for is the more useful thing to say to somebody without one.
 */
export function emptyFeedCopy(input: {
  filtered: boolean;
  hidden: number;
  hasFleet: boolean;
}): EmptyFeedCopy {
  if (input.filtered) {
    const { hidden } = input;
    return {
      title: 'Nothing in the kinds you are watching',
      description: `${pluralise(hidden, 'kind')} of event ${hidden === 1 ? 'is' : 'are'} switched off for this browser. Choose above turns them back on.`,
    };
  }
  return {
    title: 'Nothing has happened yet',
    description: input.hasFleet
      ? 'A line is written here every time a job finishes, a runner comes up or goes away, the scheduler decides something, or a host, machine or pool changes underneath them.'
      : 'Once there is a pool and a host, this is where the fleet says what it has been doing.',
  };
}
