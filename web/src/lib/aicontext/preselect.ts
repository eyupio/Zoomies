/*
 * Setup opened from a repository's page.
 *
 * A repository's AI Context tab links to setup with the repository's GitHub ID,
 * and the wizard ticks it once, when the installation's list of repositories
 * arrives. Anything else about choosing repositories stays as it was: nothing is
 * preselected unless a person came from one.
 *
 * The address is anybody's to write, so the ID in it is trusted only as far as it
 * looks like one of GitHub's, and then only to be looked for in the list the server
 * returned. It selects nothing that list does not hold, and selecting is not
 * publishing: setup still ends in a pull request somebody reviews.
 */

/**
 * The repository an address names: a positive whole number, as GitHub's IDs are,
 * or nothing. Past the largest integer a number holds exactly, two IDs would be
 * one, so those name nothing either.
 */
export function repositoryIdFromAddress(raw: string): number | undefined {
  if (!/^[1-9]\d*$/.test(raw)) return undefined;
  const id = Number(raw);
  return Number.isSafeInteger(id) ? id : undefined;
}

/** What the installation step says about choosing, given the repository the person came from, if any. */
export function installationHint(cameFrom: string | null): string {
  return cameFrom
    ? `Choose one installation at a time. ${cameFrom} is selected because you came from its page.`
    : 'Choose one installation at a time. No repository is preselected.';
}

// Said instead of nothing: a person who followed a link from a repository and
// finds none chosen would otherwise wonder what the link was for.

/** The repository the person came from is not in the list the server returned. */
export const NOT_FOUND_NOTE =
  "The repository you came from is not among this installation's repositories, so none is selected.";

/** The repository the person came from is listed, but setup is not offered for an archived one. */
export function archivedNote(name: string): string {
  return `${name} is archived and cannot be set up, so none is selected.`;
}
