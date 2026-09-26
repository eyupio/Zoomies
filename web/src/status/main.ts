/**
 * Entry point for /status, the page for readers with no account.
 *
 * Its own Vite entry rather than a route of the app, so it pulls in the
 * tokens and this one component and nothing else: no app shell, no router, no
 * event stream. vite.config.ts holds it to its own, much smaller, budget.
 */
import { mount } from 'svelte';
import '../lib/styles/tokens.css';
import StatusPage from './StatusPage.svelte';

// The operator's own theme choice, when this browser has made one; the
// system setting otherwise, which tokens.css follows with no attribute. Done
// here rather than inline in status.html because the page's CSP carries no
// hash for an inline script of its own.
try {
  const theme = localStorage.getItem('zoomies.theme');
  if (theme === 'dark' || theme === 'light') {
    document.documentElement.setAttribute('data-theme', theme);
  }
} catch {
  /* private mode: the system preference stands */
}

const target = document.getElementById('status');
if (!target) {
  throw new Error('The #status element is missing from status.html; the page cannot mount.');
}

export default mount(StatusPage, { target });
