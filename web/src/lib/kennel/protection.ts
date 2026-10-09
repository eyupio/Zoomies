/**
 * What the Protection tab is about, and where on GitHub each finding is fixed.
 *
 * The four checks read a repository's settings and the status checks its default
 * branch requires. A fix for each is a setting on GitHub and not a file, so the
 * tab links to the page the setting is on. The link is built here from three
 * things and nothing else: a fixed path for the check, the address GitHub's web
 * pages are served from for this installation, and the repository's name once it
 * has the shape of one. Nothing a repository wrote is ever put in an address.
 */

/** The checks that read the repository's settings or its required status checks. */
export const PROTECTION_CHECKS = [
  'token.default_write',
  'exposure.fork_approval_weak',
  'exposure.private_fork_secrets',
  'protection.required_check_never_reports',
] as const;

/** The sources they read, whose coverage the tab shows. */
export const PROTECTION_SOURCES = ['settings', 'protection'] as const;

/** Whether a finding is one the Protection tab is about. */
export function isProtectionCheck(code: string): boolean {
  return (PROTECTION_CHECKS as readonly string[]).includes(code);
}

/** Whether any of the four checks is on. All four are off with `kennel.settings_checks`. */
export function protectionChecksOn(disabled: readonly string[]): boolean {
  return !PROTECTION_CHECKS.every((code) => disabled.includes(code));
}

interface SettingsPage {
  label: string;
  /** Under the repository's own address on GitHub. */
  path: string;
}

const ACTIONS: SettingsPage = { label: 'Actions settings', path: '/settings/actions' };

const PAGES: Record<(typeof PROTECTION_CHECKS)[number], SettingsPage[]> = {
  'token.default_write': [ACTIONS],
  'exposure.fork_approval_weak': [ACTIONS],
  'exposure.private_fork_secrets': [ACTIONS],
  // A branch can be protected by a classic rule or by a ruleset, and a person
  // fixing a check cannot tell which from here, so both pages are offered.
  'protection.required_check_never_reports': [
    { label: 'Branch protection', path: '/settings/branches' },
    { label: 'Rulesets', path: '/settings/rules' },
  ],
};

export interface SettingsLink {
  label: string;
  href: string;
}

/** `owner/name`, with the characters GitHub allows in each and nothing else. */
const REPOSITORY = /^[A-Za-z0-9_.-]{1,100}\/[A-Za-z0-9_.-]{1,100}$/;

/**
 * The pages on GitHub where a finding's setting is changed, or none when an
 * address cannot be built safely.
 *
 * `webURL` is the installation's GitHub base, which differs on an Enterprise
 * host; without a plain http or https address, or with a repository name that
 * does not look like one, there is no link rather than a guessed one.
 */
export function settingsLinks(
  code: string,
  webURL: string | undefined,
  repository: string,
): SettingsLink[] {
  const pages = (PAGES as Record<string, SettingsPage[] | undefined>)[code];
  if (!pages || !webURL || !REPOSITORY.test(repository)) return [];
  let base: URL;
  try {
    base = new URL(webURL);
  } catch {
    return [];
  }
  if (base.protocol !== 'https:' && base.protocol !== 'http:') return [];
  const root = base.origin + base.pathname.replace(/\/+$/, '');
  return pages.map((page) => ({ label: page.label, href: `${root}/${repository}${page.path}` }));
}
