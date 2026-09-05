/**
 * A History API router, small enough to read in one sitting.
 *
 * Two things make it worth having rather than reaching for a framework:
 *
 *  * Route components are loaded with dynamic `import()`, so the grids, the
 *    log viewer and the pool wizard never reach a visitor who only looks at the
 *    Overview. That is most of how the app shell stays under budget.
 *  * Query state is first class. Every grid keeps its filters and paging in the
 *    URL so a view can be pasted into a chat window, and `setQuery` replaces
 *    them without unmounting the page.
 *
 * Reactivity comes from `createSubscriber`, which lets a plain `.ts` module
 * take part in Svelte's dependency tracking without being a `.svelte.ts`.
 */
import { createSubscriber } from 'svelte/reactivity';
import type { Component } from 'svelte';
import Login from '../routes/Login.svelte';
import NotFound from '../routes/NotFound.svelte';

export interface RouteDef {
  name: string;
  /** The pattern, with `:param` segments. */
  path: string;
  /** The page name, used for the top bar and the route-change announcement. */
  title: string;
  /** Eagerly bundled pages (the unauthenticated ones) pass `component` instead. */
  component?: Component<Record<string, never>>;
  load?: () => Promise<{ default: Component<Record<string, never>> }>;
}

/**
 * Order matters: the first match wins, so `/pools/new` is listed before
 * `/pools/:id` or creating a pool would try to open one called "new".
 */
export const ROUTES: readonly RouteDef[] = [
  {
    name: 'overview',
    path: '/',
    title: 'Overview',
    load: () => import('../routes/Overview.svelte'),
  },
  { name: 'pools', path: '/pools', title: 'Pools', load: () => import('../routes/Pools.svelte') },
  {
    name: 'pool-new',
    path: '/pools/new',
    title: 'Create a pool',
    load: () => import('../routes/PoolWizard.svelte'),
  },
  {
    name: 'pool',
    path: '/pools/:id',
    title: 'Pool',
    load: () => import('../routes/PoolDetail.svelte'),
  },
  {
    name: 'runners',
    path: '/runners',
    title: 'Runners',
    load: () => import('../routes/Runners.svelte'),
  },
  {
    name: 'runner',
    path: '/runners/:id',
    title: 'Runner',
    load: () => import('../routes/RunnerDetail.svelte'),
  },
  { name: 'jobs', path: '/jobs', title: 'Jobs', load: () => import('../routes/Jobs.svelte') },
  { name: 'hosts', path: '/hosts', title: 'Hosts', load: () => import('../routes/Hosts.svelte') },
  {
    name: 'host-new',
    path: '/hosts/new',
    title: 'Add a host',
    load: () => import('../routes/AddHost.svelte'),
  },
  {
    name: 'installations',
    path: '/installations',
    title: 'Installations',
    load: () => import('../routes/Installations.svelte'),
  },
  {
    name: 'migrate',
    path: '/migrate',
    title: 'Migrate repositories',
    load: () => import('../routes/Migrate.svelte'),
  },
  { name: 'audit', path: '/audit', title: 'Audit', load: () => import('../routes/Audit.svelte') },
  {
    name: 'settings',
    path: '/settings',
    title: 'Settings',
    load: () => import('../routes/Settings.svelte'),
  },
  {
    // GitHub's return address, named in every App manifest this controller
    // builds. It hands what GitHub sent to the Installations page; it is not
    // somewhere anybody navigates to on purpose.
    name: 'github-setup',
    path: '/settings/github/setup',
    title: 'Connecting GitHub',
    load: () => import('../routes/GithubSetup.svelte'),
  },
  {
    name: 'login',
    path: '/login',
    title: 'Sign in',
    component: Login as Component<Record<string, never>>,
  },
];

const NOT_FOUND: RouteDef = {
  name: 'not-found',
  path: '*',
  title: 'Page not found',
  component: NotFound as Component<Record<string, never>>,
};

/* -- matching -------------------------------------------------------------- */

/**
 * A path segment as typed, or as it was if it is not valid percent-encoding.
 * decodeURIComponent throws on a stray `%`, and a throw here would leave the
 * app on its loading skeleton for good; an id that does not decode simply
 * matches nothing, and the page says so.
 */
function decodeSegment(v: string): string {
  try {
    return decodeURIComponent(v);
  } catch {
    return v;
  }
}

function match(pathname: string): { route: RouteDef; params: Record<string, string> } {
  const parts = pathname.replace(/\/+$/, '').split('/').filter(Boolean);
  for (const route of ROUTES) {
    const pattern = route.path.split('/').filter(Boolean);
    if (pattern.length !== parts.length) continue;
    const params: Record<string, string> = {};
    let ok = true;
    for (let i = 0; i < pattern.length; i++) {
      const p = pattern[i] ?? '';
      const v = parts[i] ?? '';
      if (p.startsWith(':')) params[p.slice(1)] = decodeSegment(v);
      else if (p !== v) {
        ok = false;
        break;
      }
    }
    if (ok) return { route, params };
  }
  return { route: NOT_FOUND, params: {} };
}

/* -- state ----------------------------------------------------------------- */

let invalidate: (() => void) | null = null;
const subscribe = createSubscriber((update) => {
  invalidate = update;
  return () => {
    invalidate = null;
  };
});

let currentPath = '/';
let currentSearch = '';
let currentRoute: RouteDef = NOT_FOUND;
let currentParams: Record<string, string> = {};
let currentComponent: Component<Record<string, never>> | null = null;
let currentTitle = '';
let loading = false;
let loadError: Error | null = null;
let navigationToken = 0;
/** Bumped on every completed navigation, so pages can key off a fresh mount. */
let navigationCount = 0;

function changed(): void {
  invalidate?.();
}

async function apply(): Promise<void> {
  const token = ++navigationToken;
  currentPath = location.pathname;
  currentSearch = location.search;
  const found = match(currentPath);
  const sameRoute = found.route === currentRoute;
  currentRoute = found.route;
  currentParams = found.params;
  currentTitle = found.route.title;
  loadError = null;

  if (found.route.component) {
    currentComponent = found.route.component;
    loading = false;
  } else if (!sameRoute || currentComponent === null) {
    loading = true;
    currentComponent = null;
    changed();
    try {
      const module = await found.route.load?.();
      if (token !== navigationToken) return;
      currentComponent = module?.default ?? null;
    } catch (cause) {
      if (token !== navigationToken) return;
      loadError =
        cause instanceof Error
          ? cause
          : new Error('That page could not be loaded. Check your connection and try again.');
    }
    loading = false;
  }

  navigationCount += 1;
  document.title = currentTitle === 'Overview' ? 'Zoomies' : `${currentTitle} · Zoomies`;
  setCanonical();
  changed();
}

/**
 * Point the page's canonical address at the route being shown.
 *
 * Every route is served the same HTML by the Go binary, so without this the
 * whole app claims to be the root. The query string is deliberately dropped:
 * grids keep their filters and paging there, and `/runners?state=busy&page=3`
 * is the runners page, not a page of its own.
 */
function setCanonical(): void {
  const href = location.origin + location.pathname;
  const link = document.querySelector<HTMLLinkElement>('link[rel="canonical"]');
  if (link) link.href = href;
  const og = document.querySelector<HTMLMetaElement>('meta[property="og:url"]');
  if (og) og.content = href;
}

/* -- link interception ------------------------------------------------------ */

function onClick(e: MouseEvent): void {
  if (e.defaultPrevented || e.button !== 0) return;
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const anchor = (e.target as HTMLElement | null)?.closest?.('a');
  if (!anchor) return;
  if (anchor.target && anchor.target !== '_self') return;
  if (anchor.hasAttribute('download') || anchor.hasAttribute('data-native')) return;
  if ((anchor.getAttribute('rel') ?? '').includes('external')) return;
  const href = anchor.getAttribute('href');
  if (!href || href.startsWith('#') || href.startsWith('mailto:')) return;
  const url = new URL(anchor.href, location.href);
  if (url.origin !== location.origin) return;
  // The API and the OpenAPI document are served by the Go binary, not by us.
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/webhooks/')) return;
  e.preventDefault();
  navigate(url.pathname + url.search + url.hash);
}

/* -- the public surface ------------------------------------------------------ */

export interface NavigateOptions {
  /** Replace the current entry instead of pushing a new one. Filters use this. */
  replace?: boolean;
  /** Keep the scroll position. Grids that change a page do. */
  keepScroll?: boolean;
}

/** Go somewhere. Same-origin paths only; use a plain link for anything external. */
export function navigate(to: string, options: NavigateOptions = {}): void {
  const url = new URL(to, location.href);
  const target = url.pathname + url.search + url.hash;
  const current = location.pathname + location.search + location.hash;
  if (target === current) return;
  if (options.replace) history.replaceState({}, '', target);
  else history.pushState({}, '', target);
  if (!options.keepScroll) window.scrollTo(0, 0);
  void apply();
}

/** Build an href with query parameters, for links that carry filter state. */
export function href(
  path: string,
  query?: Record<string, string | number | undefined | null>,
): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) {
    if (v === undefined || v === null || v === '') continue;
    params.set(k, String(v));
  }
  const s = params.toString();
  return s ? `${path}?${s}` : path;
}

export const router = {
  /** Start listening. Returns the teardown, for symmetry; the shell never calls it. */
  start(): () => void {
    addEventListener('popstate', () => void apply());
    document.addEventListener('click', onClick);
    void apply();
    return () => {
      document.removeEventListener('click', onClick);
    };
  },

  get pathname(): string {
    subscribe();
    return currentPath;
  },
  get route(): RouteDef {
    subscribe();
    return currentRoute;
  },
  get params(): Record<string, string> {
    subscribe();
    return currentParams;
  },
  /** The page component, or null while its chunk is still in flight. */
  get component(): Component<Record<string, never>> | null {
    subscribe();
    return currentComponent;
  },
  get loading(): boolean {
    subscribe();
    return loading;
  },
  get error(): Error | null {
    subscribe();
    return loadError;
  },
  /** The page name shown in the top bar and announced on navigation. */
  get title(): string {
    subscribe();
    return currentTitle;
  },
  /** Increments once per completed navigation. Handy as a `{#key}` value. */
  get navigation(): number {
    subscribe();
    return navigationCount;
  },

  /** A detail page can name itself once it knows what it is looking at. */
  setTitle(title: string): void {
    if (currentTitle === title) return;
    currentTitle = title;
    document.title = `${title} · Zoomies`;
    changed();
  },

  /* -- query state ------------------------------------------------------- */

  /** The current query string, parsed. Reactive: reading it in a page tracks it. */
  get query(): URLSearchParams {
    subscribe();
    return new URLSearchParams(currentSearch);
  },

  /** One query parameter, or the fallback when it is absent. */
  param(name: string, fallback = ''): string {
    subscribe();
    return new URLSearchParams(currentSearch).get(name) ?? fallback;
  },

  /** A repeated query parameter, as the API's `explode: true` style produces. */
  paramList(name: string): string[] {
    subscribe();
    return new URLSearchParams(currentSearch).getAll(name);
  },

  /** A numeric query parameter, clamped to a sensible fallback. */
  paramNumber(name: string, fallback: number): number {
    subscribe();
    const raw = new URLSearchParams(currentSearch).get(name);
    const n = raw === null ? Number.NaN : Number(raw);
    return Number.isFinite(n) ? n : fallback;
  },

  /**
   * Merge into the query string without a full navigation. `null` removes a
   * key, an array repeats it. Replaces the history entry by default, so
   * typing in a filter does not fill the back button with keystrokes.
   */
  setQuery(
    patch: Record<string, string | number | boolean | readonly string[] | null | undefined>,
    options: NavigateOptions = {},
  ): void {
    const params = new URLSearchParams(currentSearch);
    for (const [key, value] of Object.entries(patch)) {
      params.delete(key);
      if (value === null || value === undefined || value === '') continue;
      if (Array.isArray(value)) for (const v of value) params.append(key, String(v));
      else params.set(key, String(value));
    }
    const search = params.toString();
    const target = `${location.pathname}${search ? `?${search}` : ''}`;
    if (target === location.pathname + location.search) return;
    if (options.replace === false) history.pushState({}, '', target);
    else history.replaceState({}, '', target);
    currentSearch = location.search;
    changed();
  },

  /** Drop every query parameter. The "clear all" in FilterBar calls this. */
  clearQuery(options: NavigateOptions = {}): void {
    if (!currentSearch) return;
    if (options.replace === false) history.pushState({}, '', location.pathname);
    else history.replaceState({}, '', location.pathname);
    currentSearch = '';
    changed();
  },

  navigate,
  href,
};
