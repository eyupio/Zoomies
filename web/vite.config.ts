import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { gzipSync } from 'node:zlib';
import { resolve } from 'node:path';

/**
 * The budgets from docs/ui-guidelines.md §5, in bytes gzipped.
 *
 * They are enforced rather than aspirational: an operator dashboard that takes
 * a second to appear on a slow VPN is a dashboard people stop opening.
 */
const SHELL_BUDGET = 200 * 1024;
const ROUTE_BUDGET = 80 * 1024;

/**
 * The status page's budget, for the whole of it: /status is its own document
 * (status.html), read by people with no account and often on a phone, and it
 * is a few words and a poll -- the tokens, one component and the Svelte
 * runtime. It is built by a second pass (`vite build --mode status`) rather
 * than as a second input to this one, because two inputs share chunks and a
 * shared chunk moved bytes into the app shell; built apart, the shell is
 * exactly what it was.
 */
const STATUS_BUDGET = 30 * 1024;

/**
 * Route chunks allowed past the route budget, with the number each is allowed.
 *
 * Named here rather than waved through: xterm.js is a terminal emulator, it is
 * what it costs, and it loads only on the two pages that show a runner's
 * output. An entry appearing in this map is a decision somebody made; a route
 * quietly growing past the budget is not.
 */
const ROUTE_ALLOWANCES: Record<string, number> = {
  xterm: 96 * 1024,
};

/** The chunk name without Vite's content hash: `assets/xterm.ScmBfeDI.js` -> `xterm`. */
function chunkName(file: string): string {
  return file.replace(/^.*\//, '').replace(/\.[^.]+\.(js|css)$/, '');
}

function budgets(label: string, limit: number): Plugin {
  return {
    name: 'zoomies-budgets',
    apply: 'build',
    generateBundle(_options, bundle) {
      // The shell is the entry chunk, everything it imports statically, and
      // the CSS those import. A route is reached by a dynamic import and is
      // not in this set -- which is the fix for a shell number that used to
      // charge every route's stylesheet to the first paint.
      const shellFiles = new Set<string>();
      const walk = (file: string): void => {
        const chunk = bundle[file];
        if (!chunk || chunk.type !== 'chunk' || shellFiles.has(file)) return;
        shellFiles.add(file);
        for (const css of chunk.viteMetadata?.importedCss ?? []) shellFiles.add(css);
        for (const imported of chunk.imports) walk(imported);
      };
      for (const [file, chunk] of Object.entries(bundle)) {
        if (chunk.type === 'chunk' && chunk.isEntry) walk(file);
      }

      let shell = 0;
      const rows: Array<[string, number]> = [];
      const overweight: string[] = [];
      for (const [file, chunk] of Object.entries(bundle)) {
        const source =
          chunk.type === 'chunk'
            ? chunk.code
            : typeof chunk.source === 'string'
              ? chunk.source
              : '';
        if (!source) continue;
        const size = gzipSync(Buffer.from(source)).length;
        const isShell = shellFiles.has(file);
        if (isShell) {
          shell += size;
        } else {
          const name = chunkName(file);
          const allowed = ROUTE_ALLOWANCES[name] ?? ROUTE_BUDGET;
          if (size > allowed) {
            overweight.push(
              `${file} is ${kb(size)} gzipped, over the ${kb(allowed)} a route may be`,
            );
          }
        }
        rows.push([`${isShell ? 'shell ' : 'route '}${file}`, size]);
      }
      rows.sort((a, b) => b[1] - a[1]);
      this.info(
        `gzipped sizes:\n${rows.map(([n, s]) => `  ${kb(s).padStart(9)}  ${n}`).join('\n')}`,
      );
      this.info(`${label}: ${kb(shell)} of ${kb(limit)} budget`);
      if (shell > limit) {
        this.error(
          `${label} is ${kb(shell)} gzipped, over the ${kb(limit)} budget in ` +
            `docs/ui-guidelines.md. Move something to a lazily loaded route, or raise the ` +
            `budget deliberately in both places.`,
        );
      }
      if (overweight.length > 0) {
        this.error(
          `${overweight.join('\n')}\n` +
            `The route budget is in docs/ui-guidelines.md. Split the route, or name it in ` +
            `ROUTE_ALLOWANCES with what it is allowed and why.`,
        );
      }
    },
  };
}

const kb = (n: number): string => `${(n / 1024).toFixed(1)} KB`;

export default defineConfig(({ mode }) => {
  const status = mode === 'status';
  return {
    plugins: [
      tailwindcss(),
      svelte(),
      status ? budgets('status page', STATUS_BUDGET) : budgets('app shell', SHELL_BUDGET),
    ],
    resolve: {
      alias: { $lib: resolve(import.meta.dirname, 'src/lib') },
    },
    build: {
      // Straight into the directory the Go binary embeds.
      outDir: '../internal/api/webdist',
      // The status pass writes beside the app's output, never over it.
      emptyOutDir: !status,
      target: 'es2022',
      sourcemap: false,
      chunkSizeWarningLimit: 300,
      rollupOptions: {
        input: resolve(import.meta.dirname, status ? 'status.html' : 'index.html'),
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
  };
});
