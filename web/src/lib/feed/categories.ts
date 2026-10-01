/**
 * What the Overview's feed can tell an operator, and which of it a browser
 * that has never been asked shows.
 *
 * The feed used to be the scheduler's decisions and nothing else, which made
 * it the only live history in the product and left every other thing worth
 * knowing -- a host that stopped answering, a machine that never became one,
 * a job whose runner died under it -- to be found by opening the page it
 * belongs to. Those are now categories here, and each is a switch on
 * Settings -> Events, because "what belongs on my dashboard" is a question
 * only the operator can answer: a fleet renting machines wants them, a fleet
 * of three fixed hosts never will.
 *
 * Two rules keep the list honest. A category is an *area of the fleet*, not an
 * event kind on the wire -- an operator thinks "hosts", not "host.updated" --
 * and a category never reports a frame that carries no news, so turning one on
 * cannot flood the panel with heartbeats. What makes a frame news is in
 * `changes.ts`.
 */
import {
  Boxes,
  Circle,
  CircleCheck,
  PawPrint,
  Play,
  Plug,
  ScrollText,
  Server,
  ServerCog,
  TrendingUp,
  TriangleAlert,
} from '@lucide/svelte';
import type { LucideIcon } from '@lucide/svelte';

export type FeedCategoryID =
  | 'scaling'
  | 'runners'
  | 'runner-lifecycle'
  | 'cpu'
  | 'jobs'
  | 'outcomes'
  | 'hosts'
  | 'machines'
  | 'pools'
  | 'github'
  | 'problems'
  | 'audit';

/**
 * The headings the settings page lists the categories under, in order. They
 * are only a way to read twelve switches without a wall of them: what a
 * category reports is its own business, and nothing branches on the group.
 */
export const FEED_GROUPS = [
  'The fleet at work',
  'Jobs',
  'Infrastructure',
  'Connections and records',
] as const;

export type FeedGroup = (typeof FEED_GROUPS)[number];

export interface FeedCategory {
  id: FeedCategoryID;
  /** What the settings row calls it. */
  label: string;
  group: FeedGroup;
  /** One line saying what an entry of this kind is. */
  description: string;
  icon: LucideIcon;
  /** Whether a browser that has never been told shows it. */
  on: boolean;
  /**
   * Whether the feed can show this category's past when a tab opens cold, or
   * only what happens from now on. It is said on the settings page because a
   * category that is on and empty otherwise looks exactly like one that is
   * broken.
   */
  history: boolean;
}

/**
 * The categories, in the order the settings page lists them and the feed's
 * filter offers them: what the fleet decided, then what went wrong, then the
 * things that change underneath it, then the record of who changed them.
 */
export const FEED_CATEGORIES: readonly FeedCategory[] = [
  {
    id: 'scaling',
    label: 'Scaling decisions',
    group: 'The fleet at work',
    description:
      'Every time the scheduler creates or removes runners, in its own words: “scaled linux-x64 2 → 4: 3 jobs queued > 30s”.',
    icon: TrendingUp,
    on: true,
    history: true,
  },
  {
    id: 'runners',
    label: 'Runner failures',
    group: 'The fleet at work',
    description:
      'A runner that stopped unexpectedly, with what to do about it. Ordinary lifecycle — provisioning, idle, busy, gone — stays on the Runners page.',
    icon: TriangleAlert,
    on: true,
    history: true,
  },
  {
    id: 'runner-lifecycle',
    label: 'Runners coming and going',
    group: 'The fleet at work',
    description:
      'A runner that registered and is ready for work, and one removed when its work was done — the ordinary, successful half of the life the failures above are the exception to. One or two lines per runner, so a fleet of ephemeral runners writes a couple per job.',
    icon: Circle,
    on: true,
    history: false,
  },
  {
    id: 'cpu',
    label: 'Elastic CPU',
    group: 'The fleet at work',
    description:
      'A runner lent spare CPU or handed it back, in the words the runner’s own page uses: squirrel spotted, rabbit spotted, leash tightened, steady paws. It is how a pool set to burst is seen doing it, and how a host under pressure is seen slowing its runners down.',
    icon: PawPrint,
    on: true,
    history: false,
  },
  {
    id: 'jobs',
    label: 'Job failures',
    group: 'Jobs',
    description:
      'A job that failed, and the step it stopped at. A job whose runner died under it is named as the fleet’s failure rather than the workflow’s, which is the distinction this page exists to make.',
    icon: Play,
    on: true,
    history: true,
  },
  {
    id: 'outcomes',
    label: 'Jobs that finished',
    group: 'Jobs',
    description:
      'Every other job GitHub reports over, with how it ended, where it came from and how long it took. The busiest category on a fleet that finishes a job a minute — switch it off and the failures above stay.',
    icon: CircleCheck,
    on: true,
    history: true,
  },
  {
    id: 'hosts',
    label: 'Hosts',
    group: 'Infrastructure',
    description:
      'A host that joined, stopped answering, came back, was cordoned, or that the fleet throttled after sustained pressure.',
    icon: Server,
    on: true,
    history: false,
  },
  {
    id: 'machines',
    label: 'Rented machines',
    group: 'Infrastructure',
    description:
      'Machines a provider is renting you: one being created, one that became a host, one that failed or was quarantined, and a provider paused or unreachable.',
    icon: ServerCog,
    on: true,
    history: false,
  },
  {
    id: 'pools',
    label: 'Pool changes',
    group: 'Infrastructure',
    description: 'A pool created, changed or deleted — the fleet’s shape, rather than its weather.',
    icon: Boxes,
    on: true,
    history: false,
  },
  {
    id: 'github',
    label: 'GitHub connection',
    group: 'Connections and records',
    description:
      'An App installation that started or stopped working, and webhook deliveries GitHub sent that Zoomies refused.',
    icon: Plug,
    on: true,
    history: false,
  },
  {
    id: 'problems',
    label: 'Problems raised',
    group: 'Connections and records',
    description:
      'Each problem the moment it is first reported. Off by default: the bell in the top bar already carries them, and this is the same list arriving twice.',
    icon: TriangleAlert,
    on: false,
    history: false,
  },
  {
    id: 'audit',
    label: 'Who changed what',
    group: 'Connections and records',
    description:
      'Every recorded action and who took it. Off by default: on a fleet with automation against the API it is the busiest thing here, and the Audit page has all of it with filters.',
    icon: ScrollText,
    on: false,
    history: true,
  },
];

/** The category an id names, or undefined for one this build does not have. */
export function feedCategory(id: string): FeedCategory | undefined {
  return FEED_CATEGORIES.find((category) => category.id === id);
}
