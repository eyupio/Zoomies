/**
 * Per-operator preferences: whether the navigation is collapsed, which columns
 * a grid shows, how many rows it asks for, and which one-off notices have been
 * put away.
 *
 * Every localStorage access is wrapped, because private-browsing modes throw on
 * access rather than returning null, and a dashboard that will not boot in a
 * private window is a dashboard someone cannot demo.
 */

const PREFS_KEY = 'zoomies.prefs';
/** Read by the inline script in index.html before first paint. */
const NAV_KEY = 'zoomies.nav.collapsed';

export const storage = {
  get(key: string): string | null {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  },
  set(key: string, value: string): void {
    try {
      localStorage.setItem(key, value);
    } catch {
      // Nothing to do and nothing worth telling the operator: the session simply
      // does not persist.
    }
  },
  remove(key: string): void {
    try {
      localStorage.removeItem(key);
    } catch {
      /* as above */
    }
  },
};

export interface GridPrefs {
  /** Column ids the operator has hidden. Stored as the exception, so new columns appear. */
  hidden?: string[];
  pageSize?: number;
}

interface StoredPrefs {
  navCollapsed?: boolean;
  grids?: Record<string, GridPrefs>;
  /**
   * Notices this browser has been told to stop showing. They are stored as a
   * list of ids rather than a flag per notice so a notice that is retired
   * leaves nothing behind, and they live here rather than on the server
   * because a nudge is one operator's business, not the fleet's.
   */
  dismissed?: string[];
}

function load(): StoredPrefs {
  const raw = storage.get(PREFS_KEY);
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? (parsed as StoredPrefs) : {};
  } catch {
    return {};
  }
}

/** The row counts a grid offers. */
export const PAGE_SIZES = [25, 50, 100, 200] as const;
export const DEFAULT_PAGE_SIZE = 50;

class Prefs {
  #navCollapsed = $state(false);
  #grids = $state<Record<string, GridPrefs>>({});
  #dismissed = $state<string[]>([]);

  constructor() {
    const stored = load();
    this.#navCollapsed = stored.navCollapsed ?? storage.get(NAV_KEY) === '1';
    this.#grids = stored.grids ?? {};
    this.#dismissed = stored.dismissed ?? [];
    this.#applyNav();
  }

  get navCollapsed(): boolean {
    return this.#navCollapsed;
  }

  set navCollapsed(value: boolean) {
    this.#navCollapsed = value;
    this.#applyNav();
    storage.set(NAV_KEY, value ? '1' : '0');
    this.#persist();
  }

  toggleNav(): void {
    this.navCollapsed = !this.#navCollapsed;
  }

  /** Column ids this grid is hiding. */
  hiddenColumns(gridId: string): string[] {
    return this.#grids[gridId]?.hidden ?? [];
  }

  isColumnVisible(gridId: string, columnId: string): boolean {
    return !this.hiddenColumns(gridId).includes(columnId);
  }

  setColumnVisible(gridId: string, columnId: string, visible: boolean): void {
    const hidden = this.hiddenColumns(gridId).filter((id) => id !== columnId);
    this.setHiddenColumns(gridId, visible ? hidden : [...hidden, columnId]);
  }

  setHiddenColumns(gridId: string, hidden: string[]): void {
    this.#grids = { ...this.#grids, [gridId]: { ...this.#grids[gridId], hidden } };
    this.#persist();
  }

  pageSize(gridId: string, fallback = DEFAULT_PAGE_SIZE): number {
    return this.#grids[gridId]?.pageSize ?? fallback;
  }

  setPageSize(gridId: string, pageSize: number): void {
    this.#grids = { ...this.#grids, [gridId]: { ...this.#grids[gridId], pageSize } };
    this.#persist();
  }

  /** Whether this browser has put a one-off notice away. */
  isDismissed(notice: string): boolean {
    return this.#dismissed.includes(notice);
  }

  dismiss(notice: string): void {
    if (this.#dismissed.includes(notice)) return;
    this.#dismissed = [...this.#dismissed, notice];
    this.#persist();
  }

  #applyNav(): void {
    if (typeof document === 'undefined') return;
    if (this.#navCollapsed) document.documentElement.setAttribute('data-nav', 'collapsed');
    else document.documentElement.removeAttribute('data-nav');
  }

  #persist(): void {
    storage.set(
      PREFS_KEY,
      JSON.stringify({
        navCollapsed: this.#navCollapsed,
        grids: this.#grids,
        dismissed: this.#dismissed,
      } satisfies StoredPrefs),
    );
  }
}

export const prefs = new Prefs();
