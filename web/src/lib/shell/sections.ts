/**
 * The sections of the product, in the one order they are ever listed in.
 *
 * The order is fixed and matches docs/ui-guidelines.md, because muscle memory
 * is the whole point of a persistent navigation -- and it is shared here rather
 * than written twice, since a phone lists the same sections in two places (the
 * bar along the bottom edge and the sheet it opens) and a list that drifts
 * between them is a list an operator cannot learn.
 *
 * The sidebar reads the sections in groups: what the fleet is doing, where it
 * runs, how it is joined to GitHub, and who looks after it. The groups change
 * nothing about the order -- they are headings over the same list, so `g j` is
 * still `g j` -- but twelve entries under four words are found by neighbourhood
 * rather than read from the top each time.
 */
import {
  Boxes,
  ChartNoAxesCombined,
  Cloud,
  GitPullRequestArrow,
  HardDrive,
  LayoutDashboard,
  ListOrdered,
  Plug,
  ScrollText,
  Server,
  Settings,
  Trophy,
  Workflow,
} from '@lucide/svelte';
import type { LucideIcon } from '@lucide/svelte';

export type NavGroupName = 'Fleet' | 'Infrastructure' | 'GitHub' | 'Administration';

export interface NavItem {
  path: string;
  label: string;
  icon: LucideIcon;
  /** The second key of the `g` chord. */
  key: string;
  /**
   * Whether the phone's bottom bar carries this one itself. Ten icons across a
   * 412px screen is a row of targets too small and too alike to hit, so the bar
   * keeps the four an operator watches a fleet with and the rest live one press
   * away in the menu.
   */
  primary?: boolean;
  /** The heading the sidebar lists it under. The Overview stands alone. */
  group?: NavGroupName;
}

export const SECTIONS: readonly NavItem[] = [
  { path: '/', label: 'Overview', icon: LayoutDashboard, key: 'o', primary: true },
  { path: '/pools', label: 'Pools', icon: Boxes, key: 'p', primary: true, group: 'Fleet' },
  { path: '/runners', label: 'Runners', icon: Server, key: 'r', primary: true, group: 'Fleet' },
  { path: '/queue', label: 'Queue', icon: ListOrdered, key: 'q', group: 'Fleet' },
  {
    path: '/workflows',
    label: 'Workflows',
    icon: Workflow,
    key: 'w',
    primary: true,
    group: 'Fleet',
  },
  { path: '/usage', label: 'Usage', icon: ChartNoAxesCombined, key: 'u', group: 'Fleet' },
  { path: '/hosts', label: 'Hosts', icon: HardDrive, key: 'h', group: 'Infrastructure' },
  { path: '/providers', label: 'Providers', icon: Cloud, key: 'v', group: 'Infrastructure' },
  { path: '/installations', label: 'Installations', icon: Plug, key: 'i', group: 'GitHub' },
  { path: '/migrate', label: 'Migrate', icon: GitPullRequestArrow, key: 'm', group: 'GitHub' },
  { path: '/kennel', label: 'Kennel Club', icon: Trophy, key: 'k', group: 'GitHub' },
  { path: '/audit', label: 'Audit', icon: ScrollText, key: 'a', group: 'Administration' },
  { path: '/settings', label: 'Settings', icon: Settings, key: 's', group: 'Administration' },
];

export interface NavGroup {
  /** Null for the sections that stand alone at the top. */
  label: NavGroupName | null;
  items: NavItem[];
}

/** The sections as the sidebar lists them: consecutive entries under one heading. */
export const NAV_GROUPS: readonly NavGroup[] = SECTIONS.reduce<NavGroup[]>((groups, item) => {
  const label = item.group ?? null;
  const last = groups[groups.length - 1];
  if (last && last.label === label) last.items.push(item);
  else groups.push({ label, items: [item] });
  return groups;
}, []);

/**
 * Pages that belong to a section without living under its path. The Jobs
 * list is the Workflows page one step down -- the same runs, opened out to
 * every job -- so the sidebar keeps Workflows lit while an operator is there.
 * AI Context lives under Kennel Club now; its old addresses still open the same
 * pages, and the sidebar lights the section they belong to from either.
 */
const WITHIN: Readonly<Record<string, string>> = {
  '/jobs': '/workflows',
  '/ai-context': '/kennel',
  '/ai-context/setup': '/kennel',
};

/** Whether `path` is the section the address bar is currently inside. */
export function isCurrentSection(path: string, here: string): boolean {
  const section = WITHIN[here] ?? here;
  return path === '/' ? section === '/' : section === path || section.startsWith(`${path}/`);
}
