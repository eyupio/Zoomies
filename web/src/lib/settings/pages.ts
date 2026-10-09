/**
 * The pages of the Settings section, in the one order they are ever listed
 * in. The rail on a desktop, the strip on a tablet, the list on a phone and
 * the account menu all read from here, so none of them can disagree about what
 * Settings holds or where a page lives.
 *
 * Three groups, by whose thing each page is. "You" is the signed-in person's
 * own account and this browser's preferences, which every role may change.
 * "Access" is who else may sign in. "Controller" is what this instance runs
 * with. The pages needing the administrator role are listed for everybody
 * regardless, marked rather than hidden: a viewer who cannot find the users
 * page should learn why, not conclude the product does not have one.
 *
 * Each page has an address of its own -- /settings/users -- so a page is a
 * link, a bookmark and a place the browser's back button returns to.
 */
import {
  Activity,
  Cable,
  CircleArrowUp,
  CircleUser,
  DatabaseBackup,
  Info,
  KeyRound,
  MessageSquare,
  Palette,
  Plug,
  SlidersHorizontal,
  Users,
} from '@lucide/svelte';
import type { LucideIcon } from '@lucide/svelte';
import type { Role } from '../api/types';

export interface SettingsPage {
  /** The last segment of the address: `users` in `/settings/users`. */
  id: string;
  label: string;
  /** One line under the label where the page is a row in a list. */
  description: string;
  icon: LucideIcon;
  /**
   * The weakest role that may open this page.
   *
   * A role rather than an "admin" flag because the pages no longer divide
   * in two: backups belong to whoever runs the process, and showing an
   * administrator a page whose every request answers 403 is worse than
   * listing it locked with the reason.
   */
  needs: Role;
}

export interface SettingsGroup {
  label: string;
  pages: readonly SettingsPage[];
}

export const SETTINGS_GROUPS: readonly SettingsGroup[] = [
  {
    label: 'You',
    pages: [
      {
        id: 'account',
        label: 'Account',
        description: 'Who you are signed in as, and your password.',
        icon: CircleUser,
        needs: 'viewer',
      },
      {
        id: 'connections',
        label: 'MCP connections',
        description: 'Claude and other MCP clients you have connected, and what each may do.',
        icon: Cable,
        needs: 'viewer',
      },
      {
        id: 'appearance',
        label: 'Appearance',
        description: 'Theme, navigation and how tables read on a phone. Kept in this browser.',
        icon: Palette,
        needs: 'viewer',
      },
      {
        id: 'events',
        label: 'Events',
        description: 'Which of the fleet’s events the Overview’s feed shows. Kept in this browser.',
        icon: Activity,
        needs: 'viewer',
      },
    ],
  },
  {
    label: 'Access',
    pages: [
      {
        id: 'users',
        label: 'Users',
        description: 'Who can sign in, and as what.',
        icon: Users,
        needs: 'admin',
      },
      {
        id: 'tokens',
        label: 'API tokens',
        description:
          'Bearer credentials for the CLI and for automation: yours, or everybody’s for an administrator.',
        icon: KeyRound,
        needs: 'viewer',
      },
      {
        id: 'mcp-clients',
        label: 'MCP clients',
        description: 'The OAuth clients that may connect to /mcp, and everybody’s connections.',
        icon: Plug,
        needs: 'admin',
      },
    ],
  },
  {
    label: 'Controller',
    pages: [
      {
        id: 'configuration',
        label: 'Configuration',
        description: 'Every setting, its value, and where the value came from.',
        icon: SlidersHorizontal,
        needs: 'admin',
      },
      {
        id: 'backups',
        label: 'Backups',
        description: 'Copies of the database, taken by hand or on a schedule.',
        icon: DatabaseBackup,
        // A backup is the whole database under the key this host holds.
        needs: 'platform',
      },
      {
        id: 'assistant',
        label: 'Assistant',
        description: 'Which model answers in the assistant, and where its traffic may go.',
        icon: MessageSquare,
        needs: 'admin',
      },
      {
        id: 'updates',
        label: 'Updates',
        description: 'Which release the update mode would take, and when.',
        icon: CircleArrowUp,
        // The status names a version, a release and a mode and nothing of the
        // fleet's, so every role may read it.
        needs: 'viewer',
      },
      {
        id: 'about',
        label: 'About',
        description: 'Zoomies, and where to read more.',
        icon: Info,
        needs: 'viewer',
      },
    ],
  },
];

export const SETTINGS_PAGES: readonly SettingsPage[] = SETTINGS_GROUPS.flatMap((g) => g.pages);

/** Where `/settings` alone lands, where there is room for the rail. */
export const DEFAULT_SETTINGS_PAGE = 'account';

/** The page an address names, or undefined for an address that names none. */
export function settingsPage(id: string): SettingsPage | undefined {
  return SETTINGS_PAGES.find((page) => page.id === id);
}

/** The address of a settings page. */
export function settingsPath(id: string): string {
  return `/settings/${id}`;
}
