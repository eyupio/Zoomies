/**
 * Entry point. Mounts the shell and nothing else -- every decision about what
 * to render lives in App.svelte, so the boot sequence is readable in one file.
 */
import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';

const target = document.getElementById('app');
if (!target) {
  throw new Error('The #app element is missing from index.html; the UI cannot mount.');
}

export default mount(App, { target });
