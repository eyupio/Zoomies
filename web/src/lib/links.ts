/**
 * Where Zoomies lives on the public internet.
 *
 * Every outbound link in the product resolves through here rather than being
 * typed at the call site, because these addresses are also the only marketing
 * this project does: an operator running their own controller is the person
 * most likely to send somebody else to it, and a dead or inconsistent link is
 * the one that never gets clicked. Keeping them in one file also means a fork
 * that renames the project changes them once.
 *
 * The documentation links point at the site rather than at Markdown files in
 * the repository. The site renders the same files, is versioned with the
 * release, and is the address worth passing on.
 */

/** The project's home page, and the canonical thing to link to. */
export const SITE_URL = 'https://zoomies.sh';

/** The source, for anyone who wants to read it before running it. */
export const REPO_URL = 'https://github.com/eyupio/zoomies';

/** The documentation site. */
export const DOCS_URL = `${SITE_URL}/`;

/** Individual pages, as the site publishes them. */
export const QUICKSTART_URL = `${SITE_URL}/quickstart/`;
export const CONFIGURATION_URL = `${SITE_URL}/configuration/`;
export const API_SURFACE_URL = `${SITE_URL}/api-surface/`;
export const SECURITY_URL = `${SITE_URL}/security/`;

/** The host name alone, for places that show a link without decoration. */
export const SITE_HOST = SITE_URL.replace(/^https?:\/\//, '');

/**
 * Who makes Zoomies. The footer of every signed-in page and the sign-in
 * colophon both say "Developed by", and the site's footer says the same from
 * `extra.developer` in mkdocs.yml, so the credit reads identically wherever
 * the product is met.
 */
export const DEVELOPER_NAME = 'EyUp.io';
export const DEVELOPER_URL = 'https://eyup.io';
