/**
 * The pages of the Kennel Club section, in the one order they are ever listed
 * in. The rail on a desktop and the strip on a narrower screen read from here, so
 * neither can disagree about what the section holds or where a page lives.
 *
 * Two groups, by what governs each page. "Standards" is what the on/off switch
 * governs: the overview and the repositories Kennel Club has checked. AI Context
 * is its own feature that happens to live under the same name: it keeps working
 * with the switch off, so it has a group of its own, apart from the switch, where
 * it would not read as one of the things the switch turns off.
 *
 * A repository's own page is under Repositories, and the AI Context setup
 * wizard under AI Context, so neither is a row of its own.
 */
export type KennelPageId = 'overview' | 'repositories' | 'ai-context';

export interface KennelPage {
  id: KennelPageId;
  label: string;
  path: string;
}

export interface KennelGroup {
  id: 'standards' | 'ai-context';
  label: string;
  /** Whether the on/off switch sits at the head of this group: it governs what is in it. */
  switch: boolean;
  pages: readonly KennelPage[];
}

export const KENNEL_GROUPS: readonly KennelGroup[] = [
  {
    id: 'standards',
    label: 'Standards',
    switch: true,
    pages: [
      { id: 'overview', label: 'Overview', path: '/kennel' },
      { id: 'repositories', label: 'Repositories', path: '/kennel/repositories' },
    ],
  },
  {
    id: 'ai-context',
    label: 'For assistants',
    switch: false,
    pages: [{ id: 'ai-context', label: 'AI Context', path: '/kennel/ai-context' }],
  },
];

/** Every page, in the order the rail lists them. */
export const KENNEL_PAGES: readonly KennelPage[] = KENNEL_GROUPS.flatMap((group) => group.pages);

/** What the list of repositories can be narrowed by from outside it: the two the cards on the Overview count. */
export interface KennelListFilter {
  state?: string;
  severity?: string;
  /**
   * List every repository, not only the ones being served. The list leaves the
   * others out unless told, so a link that counts every repository has to say so, or
   * it opens on fewer rows than the number it came from.
   */
  everything?: boolean;
  /**
   * List the repositories Kennel Club has been told not to look at. The list leaves
   * them out unless told, because the cards count the ones it is looking at.
   */
  notTracked?: boolean;
}

/**
 * The address of the list of repositories, narrowed. The list reads its filters
 * from the address, so a link is the whole of "show me these", and a colleague can
 * be sent the same one. An empty value is left out, so no filter is `/kennel/repositories`
 * and not `/kennel/repositories?state=`.
 */
export function kennelListHref(filter: KennelListFilter = {}): string {
  const base = KENNEL_PAGES.find((page) => page.id === 'repositories')!.path;
  const query = new URLSearchParams();
  if (filter.state) query.set('state', filter.state);
  if (filter.severity) query.set('severity', filter.severity);
  if (filter.notTracked) query.set('tracked', 'false');
  if (filter.everything) query.set('active', 'all');
  const text = query.toString();
  return text ? `${base}?${text}` : base;
}
