/**
 * Keyboard plumbing: the layer stack, the focus trap, and the global shortcuts
 * from docs/ui-guidelines.md section 4.
 *
 * The layer stack exists so that Escape closes exactly one thing. A dropdown
 * inside a dialog inside the command palette must unwind in the order it was
 * opened, and every overlay in the inventory registers itself here rather than
 * listening for Escape on its own.
 */

/** Overlay kinds, in the order they tend to nest. */
export type LayerKind = 'dropdown' | 'palette' | 'sheet' | 'drawer' | 'dialog';

export interface Layer {
  id: number;
  kind: LayerKind;
  close: () => void;
}

let nextLayerId = 1;
const stack: Layer[] = [];

export const layers = {
  /** Register an open overlay. Returns the id to hand back to `remove`. */
  push(kind: LayerKind, close: () => void): number {
    const id = nextLayerId++;
    stack.push({ id, kind, close });
    return id;
  },
  /** Deregister an overlay, whether it closed itself or was closed for it. */
  remove(id: number): void {
    const i = stack.findIndex((l) => l.id === id);
    if (i >= 0) stack.splice(i, 1);
  },
  top(): Layer | undefined {
    return stack[stack.length - 1];
  },
  /** Close the topmost overlay. Returns false when there was nothing to close. */
  closeTop(): boolean {
    const layer = stack[stack.length - 1];
    if (!layer) return false;
    layer.close();
    return true;
  },
  get size(): number {
    return stack.length;
  },
};

/* -- focus ---------------------------------------------------------------- */

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
  'summary',
].join(',');

/** Every focusable descendant, in document order, skipping hidden ones. */
export function focusableWithin(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => el.offsetParent !== null || el.getClientRects().length > 0,
  );
}

/**
 * A Svelte action that traps Tab inside `node` and restores focus to whatever
 * was focused before, on teardown. Dialogs and drawers both use it; nothing
 * else should need to.
 */
export function trapFocus(node: HTMLElement, initial?: HTMLElement | null) {
  const restoreTo = document.activeElement as HTMLElement | null;

  function focusFirst(): void {
    const target =
      initial ??
      node.querySelector<HTMLElement>('[data-autofocus]') ??
      focusableWithin(node)[0] ??
      node;
    target.focus();
  }

  function onKeydown(e: KeyboardEvent): void {
    if (e.key !== 'Tab') return;
    const items = focusableWithin(node);
    if (items.length === 0) {
      e.preventDefault();
      node.focus();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    if (!first || !last) return;
    const active = document.activeElement;
    if (e.shiftKey && (active === first || !node.contains(active))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  if (!node.hasAttribute('tabindex')) node.setAttribute('tabindex', '-1');
  node.addEventListener('keydown', onKeydown);
  // A frame's delay lets a transition finish laying the panel out first.
  requestAnimationFrame(focusFirst);

  return {
    destroy(): void {
      node.removeEventListener('keydown', onKeydown);
      if (restoreTo && document.contains(restoreTo)) restoreTo.focus();
    },
  };
}

/* -- global shortcuts ------------------------------------------------------ */

export const isMac =
  typeof navigator !== 'undefined' &&
  /mac|iphone|ipad/i.test(navigator.platform || navigator.userAgent);

/** The modifier label for this platform: ⌘ on a Mac, Ctrl everywhere else. */
export const modKey = isMac ? '⌘' : 'Ctrl';

/** True when the event target is somewhere a bare letter means "type a letter". */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

/** The `g` then letter jumps, in the order the navigation lists them. */
export const GO_KEYS: ReadonlyArray<{ key: string; path: string; label: string }> = [
  { key: 'o', path: '/', label: 'Overview' },
  { key: 'p', path: '/pools', label: 'Pools' },
  { key: 'r', path: '/runners', label: 'Runners' },
  { key: 'j', path: '/jobs', label: 'Jobs' },
  { key: 'h', path: '/hosts', label: 'Hosts' },
  { key: 'i', path: '/installations', label: 'Installations' },
  { key: 'a', path: '/audit', label: 'Audit' },
  { key: 's', path: '/settings', label: 'Settings' },
];

export interface ShortcutGroup {
  title: string;
  items: ReadonlyArray<{ keys: string[]; description: string }>;
}

/** What the shortcut sheet shows. The single description of the key map. */
export const SHORTCUTS: readonly ShortcutGroup[] = [
  {
    title: 'Anywhere',
    items: [
      { keys: [modKey, 'K'], description: 'Open the command palette' },
      { keys: ['/'], description: 'Focus the search on this page' },
      { keys: ['?'], description: 'Open this list' },
      { keys: ['Esc'], description: 'Close the topmost dialog, drawer or menu' },
    ],
  },
  {
    title: 'Go to',
    items: GO_KEYS.map((g) => ({ keys: ['G', g.key.toUpperCase()], description: g.label })),
  },
  {
    title: 'In a grid',
    items: [
      { keys: ['↑', '↓'], description: 'Move between rows' },
      { keys: ['Enter'], description: 'Open the focused row' },
      { keys: ['Space'], description: 'Select the focused row' },
      { keys: ['Shift', '↑/↓'], description: 'Extend the selection' },
      { keys: ['Home', 'End'], description: 'Jump to the first or last row' },
    ],
  },
];

export interface ShortcutActions {
  palette: () => void;
  help: () => void;
  search: () => void;
  go: (path: string) => void;
}

/** How long the `g` prefix stays armed before it gives up. */
const CHORD_MS = 1200;

/**
 * Install the global key map. Returns the teardown function.
 *
 * Escape is handled here and nowhere else, so exactly one layer closes per
 * press regardless of how many overlays are open.
 */
export function installShortcuts(actions: ShortcutActions): () => void {
  let armed = false;
  let armedTimer: ReturnType<typeof setTimeout> | null = null;

  function disarm(): void {
    armed = false;
    if (armedTimer) clearTimeout(armedTimer);
    armedTimer = null;
  }

  function onKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      if (layers.closeTop()) e.preventDefault();
      disarm();
      return;
    }

    const mod = isMac ? e.metaKey : e.ctrlKey;
    if (mod && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      disarm();
      actions.palette();
      return;
    }

    if (e.altKey || e.ctrlKey || e.metaKey) return;
    if (isTypingTarget(e.target)) return;

    if (armed) {
      const target = GO_KEYS.find((g) => g.key === e.key.toLowerCase());
      disarm();
      if (target) {
        e.preventDefault();
        actions.go(target.path);
      }
      return;
    }

    if (e.key === 'g') {
      armed = true;
      armedTimer = setTimeout(disarm, CHORD_MS);
      return;
    }
    if (e.key === '/') {
      e.preventDefault();
      actions.search();
      return;
    }
    if (e.key === '?') {
      e.preventDefault();
      actions.help();
    }
  }

  window.addEventListener('keydown', onKeydown);
  return () => {
    disarm();
    window.removeEventListener('keydown', onKeydown);
  };
}

/* -- search focus ---------------------------------------------------------
 * `/` focuses "the current page's search". Pages register their input here so
 * the shell does not have to know what a page looks like.
 * --------------------------------------------------------------------- */

let searchInput: HTMLElement | null = null;

/** Register this page's search field. Call the returned function on teardown. */
export function registerSearch(el: HTMLElement | null): () => void {
  searchInput = el;
  return () => {
    if (searchInput === el) searchInput = null;
  };
}

/** Focus the registered search field. Returns false when the page has none. */
export function focusSearch(): boolean {
  if (!searchInput || !document.contains(searchInput)) return false;
  searchInput.focus();
  if (searchInput instanceof HTMLInputElement) searchInput.select();
  return true;
}

/* -- scroll lock -----------------------------------------------------------
 * Shared by Dialog and Drawer, and counted, so closing an inner dialog does not
 * unlock the page while an outer one is still open.
 * --------------------------------------------------------------------- */

/** Freeze background scrolling. Returns the function that releases it. */
export function lockScroll(): () => void {
  if (typeof document === 'undefined') return () => {};
  const root = document.documentElement;
  const depth = Number(root.dataset.scrollLock ?? '0') + 1;
  root.dataset.scrollLock = String(depth);
  document.body.style.overflow = 'hidden';
  let released = false;
  return () => {
    if (released) return;
    released = true;
    const next = Number(root.dataset.scrollLock ?? '1') - 1;
    if (next <= 0) {
      delete root.dataset.scrollLock;
      document.body.style.overflow = '';
    } else {
      root.dataset.scrollLock = String(next);
    }
  };
}
