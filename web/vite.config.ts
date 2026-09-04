import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { gzipSync } from 'node:zlib';
import { resolve } from 'node:path';

/**
 * The app shell budget from docs/ui-guidelines.md, in bytes gzipped.
 *
 * This is enforced rather than aspirational: an operator dashboard that takes a
 * second to appear on a slow VPN is a dashboard people stop opening. Route
 * chunks are excluded because they load on demand.
 */
const SHELL_BUDGET = 200 * 1024;

function shellBudget(): Plugin {
  return {
    name: 'zoomies-shell-budget',
    apply: 'build',
    generateBundle(_options, bundle) {
      let shell = 0;
      const rows: Array<[string, number]> = [];
      for (const [name, chunk] of Object.entries(bundle)) {
        const source =
          chunk.type === 'chunk' ? chunk.code : typeof chunk.source === 'string' ? chunk.source : '';
        if (!source) continue;
        const size = gzipSync(Buffer.from(source)).length;
        // The entry chunk and every CSS file are the shell; everything else is
        // a lazily loaded route.
        const isShell = (chunk.type === 'chunk' && chunk.isEntry) || name.endsWith('.css');
        if (isShell) shell += size;
        rows.push([`${isShell ? 'shell ' : 'route '}${name}`, size]);
      }
      rows.sort((a, b) => b[1] - a[1]);
      const kb = (n: number) => `${(n / 1024).toFixed(1)} KB`;
      this.info(`gzipped sizes:\n${rows.map(([n, s]) => `  ${kb(s).padStart(9)}  ${n}`).join('\n')}`);
      this.info(`app shell: ${kb(shell)} of ${kb(SHELL_BUDGET)} budget`);
      if (shell > SHELL_BUDGET) {
        this.error(
          `app shell is ${kb(shell)} gzipped, over the ${kb(SHELL_BUDGET)} budget in ` +
            `docs/ui-guidelines.md. Move something to a lazily loaded route, or raise the ` +
            `budget deliberately in both places.`,
        );
      }
    },
  };
}

export default defineConfig({
  plugins: [tailwindcss(), svelte(), shellBudget()],
  resolve: {
    alias: { $lib: resolve(import.meta.dirname, 'src/lib') },
  },
  build: {
    // Straight into the directory the Go binary embeds.
    outDir: '../internal/api/webdist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    chunkSizeWarningLimit: 300,
    rollupOptions: {
      output: {
        // Content-hashed so the Go server can serve them immutable.
        entryFileNames: 'assets/[name].[hash].js',
        chunkFileNames: 'assets/[name].[hash].js',
        assetFileNames: 'assets/[name].[hash][extname]',
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      // `make dev` runs a controller on 8080; the Vite dev server proxies the
      // API to it so the UI can be developed with hot reload.
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false, ws: false },
      '/webhooks': { target: 'http://127.0.0.1:8080' },
      '/metrics': { target: 'http://127.0.0.1:8080' },
      '/healthz': { target: 'http://127.0.0.1:8080' },
    },
  },
});
