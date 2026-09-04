/**
 * Light, dark or whatever the operating system says.
 *
 * `system` is the default and writes no attribute, so `prefers-color-scheme`
 * decides. An explicit choice writes `data-theme` on `<html>` and persists. The
 * inline script in index.html applies the stored value before first paint; this
 * module only keeps it in sync afterwards -- it deliberately does not repeat
 * that work.
 */
import { storage } from './prefs.svelte';

export type ThemeChoice = 'light' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'dark';

const KEY = 'zoomies.theme';

function stored(): ThemeChoice {
  const value = storage.get(KEY);
  return value === 'light' || value === 'dark' ? value : 'system';
}

class Theme {
  #choice = $state<ThemeChoice>('system');
  #systemDark = $state(false);

  constructor() {
    this.#choice = stored();
    if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
      const query = window.matchMedia('(prefers-color-scheme: dark)');
      this.#systemDark = query.matches;
      query.addEventListener('change', (e) => {
        this.#systemDark = e.matches;
      });
    }
  }

  /** What the operator chose, including `system`. */
  get choice(): ThemeChoice {
    return this.#choice;
  }

  /** What is actually on screen. */
  get resolved(): ResolvedTheme {
    if (this.#choice === 'system') return this.#systemDark ? 'dark' : 'light';
    return this.#choice;
  }

  set(choice: ThemeChoice): void {
    this.#choice = choice;
    if (typeof document === 'undefined') return;
    if (choice === 'system') {
      document.documentElement.removeAttribute('data-theme');
      storage.remove(KEY);
    } else {
      document.documentElement.setAttribute('data-theme', choice);
      storage.set(KEY, choice);
    }
  }

  /** light → dark → system → light. What the top bar's toggle does. */
  cycle(): void {
    this.set(this.#choice === 'light' ? 'dark' : this.#choice === 'dark' ? 'system' : 'light');
  }
}

export const theme = new Theme();
