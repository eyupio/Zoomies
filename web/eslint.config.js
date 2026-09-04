import js from '@eslint/js';
import ts from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';

export default ts.config(
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs.recommended,
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
    rules: {
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
      // Explicit any is a code smell here; the API client is generated and typed.
      '@typescript-eslint/no-explicit-any': 'warn',
      eqeqeq: ['error', 'always', { null: 'ignore' }],
    },
  },
  {
    // `.svelte.ts` modules (the runes-based stores) are parsed by the Svelte
    // parser too, so they need the TypeScript parser handed to it as well --
    // without this they fail to parse rather than fail to lint.
    files: ['**/*.svelte', '**/*.svelte.ts', '**/*.svelte.js'],
    languageOptions: { parserOptions: { parser: ts.parser } },
  },
  {
    ignores: [
      'dist/',
      'node_modules/',
      'src/lib/api/schema.d.ts',
      'playwright-report/',
      'test-results/',
    ],
  },
);
