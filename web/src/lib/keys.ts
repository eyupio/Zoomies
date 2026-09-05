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

/** The panels currently trapping focus, outermost first. */
const traps: HTMLElement[] = [];

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
  traps.push(node);

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
    // Only the innermost open overlay owns Tab. Two traps on separate
    // subtrees -- a confirmation dialog raised from inside a drawer -- each
    // pulled focus into their own panel, so every Tab in the dialog landed
    // on its first control.
    if (traps[traps.length - 1] !== node) return;
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
    // Focus can be outside the panel entirely -- the browser blurs an element
    // that becomes hidden or unavailable, and lands on <body>. From there a
    // Tab would walk the page behind an open modal, so it is pulled back in
    // rather than let go.
    if (!node.contains(active)) {
      e.preventDefault();
      (e.shiftKey ? last : first).focus();
      return;
    }
    if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  if (!node.hasAttribute('tabindex')) node.setAttribute('tabindex', '-1');
  // Listened for on the document, in capture, rather than on the panel: a
  // keystroke made while focus has slipped outside the panel never reaches a
  // handler bound to the panel, which is exactly the case the pull-back above
  // exists to answer.
  document.addEventListener('keydown', onKeydown, true);
  // A frame's delay lets a transition finish laying the panel out first.
  requestAnimationFrame(focusFirst);

  return {
    destroy(): void {
      document.removeEventListener('keydown', onKeydown, true);
      const i = traps.lastIndexOf(node);
      if (i >= 0) traps.splice(i, 1);
      // A frame's delay, because the overlay's own teardown also clears the
      // `inert` it put on the rest of the page, and an inert element cannot
      // take focus -- restoring in the same turn silently did nothing.
      requestAnimationFrame(() => {
        // An overlay opened by a redirect rather than a click -- GitHub sending
        // the operator back with a code in the URL -- was focused on <body>
        // when it mounted, and focusing <body> back is a no-op that leaves the
        // keyboard at the very top of the document. Fall back to the page's own
        // landing points, the same chain a route change uses.
        const target =
          restoreTo && restoreTo !== document.body && document.contains(restoreTo)
            ? restoreTo
            : (document.getElementById('page-heading') ?? document.getElementById('main'));
        target?.focus();
      });
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
  { key: 'u', path: '/usage', label: 'Usage' },
  { key: 'h', path: '/hosts', label: 'Hosts' },
  { key: 'i', path: '/installations', label: 'Installations' },
  { key: 'm', path: '/migrate', label: 'Migrate repositories' },
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
    // Under an open overlay the keyboard belongs to it. Escape is the one
    // shortcut the shell still answers; `g r` typed into a confirmation
    // dialog must not navigate away from the thing being confirmed, and the
    // palette must not open over a dialog that is waiting for an answer.
    if (layers.size > 0) {
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
/**
 * Mark everything outside an overlay unavailable, and undo it on release.
 *
 * A focus trap stops Tab leaving a modal, but it says nothing to a screen
 * reader's virtual cursor: without this, an operator reading down from the
 * Connect GitHub dialog carries straight on into the installation cards, the
 * webhook panel and the navigation behind it, with no sign they have left the
 * dialog, and can activate a control there. `inert` is the one attribute that
 * removes an element from both the accessibility tree and the tab order.
 *
 * Every overlay marks what is outside it and unmarks exactly that on release,
 * so a dialog opened from inside a drawer takes the drawer's controls out of
 * the tree while it is up and gives them back when it closes, with the drawer
 * itself still holding the rest of the page inert. An earlier version marked
 * only at the first depth, which left the drawer's own controls live under
 * the dialog.
 */
export function pageInert(except: HTMLElement | null): () => void {
  if (typeof document === 'undefined' || !except) return () => {};

  // Walk from the overlay up to <body>, marking every sibling on the way.
  // Inerting only the top-level children would do nothing here: the app mounts
  // into #app and the dialog renders inside it, so #app is the one child that
  // contains the overlay and would be skipped, leaving the whole page live.
  // A sibling that is already inert belongs to an outer overlay, which will
  // release it; skipping it here is what keeps the two releases apart.
  const marked: HTMLElement[] = [];
  for (let node: HTMLElement | null = except; node && node !== document.body;) {
    const parent: HTMLElement | null = node.parentElement;
    if (!parent) break;
    for (const sibling of parent.children) {
      if (sibling === node || !(sibling instanceof HTMLElement) || sibling.inert) continue;
      // A live region has to stay announceable: a toast raised while a
      // dialog is open is usually about the dialog.
      if (sibling.hasAttribute('data-inert-exempt')) continue;
      sibling.inert = true;
      marked.push(sibling);
    }
    node = parent;
  }

  let released = false;
  return () => {
    if (released) return;
    released = true;
    for (const el of marked) el.inert = false;
  };
}

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
