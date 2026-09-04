import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    // Runes mode everywhere. This is a Svelte 5 codebase; the legacy reactive
    // syntax is not used and should not compile.
    runes: true,
  },
};
