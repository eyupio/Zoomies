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
