import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  // Build artifacts and vendored third-party bundles are not our source — they
  // are minified / generated and should never be linted. `output/` is the
  // build staging dir (gitignored), `dist/` the Vite output, and `vendor/`
  // dirs hold upstream libraries shipped verbatim with the single-file apps.
  globalIgnores([
    'dist',
    'output',
    'apps/*/vendor/**',
    'apps/**/vendor/**',
    'apps/notes/collab.js',
    'apps/social/hls.js',
    'public/sw.js',
  ]),
  {
    files: ['**/*.{js,jsx}'],
    extends: [
      js.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      // Browser is the primary runtime, but a handful of shell globals
      // (service-worker registration, React DevTools hook, the Cache API used
      // by the offline layer) are not in the default browser set on every
      // eslint/globals version, so they are added explicitly here.
      globals: {
        ...globals.browser,
        __REACT_DEVTOOLS_GLOBAL_HOOK__: 'readonly',
        caches: 'readonly',
      },
      parserOptions: {
        ecmaVersion: 'latest',
        ecmaFeatures: { jsx: true },
        sourceType: 'module',
      },
    },
    rules: {
      'no-unused-vars': ['error', { varsIgnorePattern: '^[A-Z_]' }],
    },
  },
  // Test files — Vitest runs with `globals: true`, so the describe/it/expect
  // family and the lifecycle hooks are ambient. Also allow Node globals
  // (__dirname, process) used by fixture-loading tests.
  // TypeScript sources (src/lib/ — the security-critical client SDK, plus the
  // generated wire types). Deliberately mirrors the JS block's react-hooks
  // rules: converting a file to TypeScript must NOT silently drop the
  // rules-of-hooks / exhaustive-deps coverage it had as .jsx. tsc catches type
  // errors; only eslint catches a conditional hook.
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      ...tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      // react-refresh was in the .js/.jsx block but NOT here, so converting a
      // file to .tsx silently dropped its HMR-safety linting — and any
      // `eslint-disable react-refresh/...` comment it carried over became a
      // hard error, since the rule no longer existed for that file type.
      // Converting a file must not quietly reduce what checks it.
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      parser: tseslint.parser,
      globals: { ...globals.browser },
      parserOptions: { ecmaVersion: 'latest', sourceType: 'module', ecmaFeatures: { jsx: true } },
    },
    rules: {
      // Superseded by the TS-aware version, which understands type-only imports.
      'no-unused-vars': 'off',
      '@typescript-eslint/no-unused-vars': ['error', { varsIgnorePattern: '^[A-Z_]' }],
    },
  },
  {
    files: ['**/*.test.{js,jsx,ts,tsx}', '**/__tests__/**/*.{js,jsx,ts,tsx}', 'src/test-setup.js'],
    languageOptions: {
      globals: {
        ...globals.vitest,
        ...globals.node,
      },
    },
  },
  // Node-context files — config, build scripts, and service worker tests read
  // from the filesystem and use CommonJS/Node globals.
  //
  // e2e/ belongs here too: the Playwright specs and the mock backend run in
  // Node (not the browser) and use Node globals such as Buffer. Without this
  // they lint as browser code and fail on `no-undef`.
  {
    files: [
      '*.config.js',
      'scripts/**/*.{js,mjs}',
      'public/**/*.js',
      'e2e/**/*.{js,mjs}',
    ],
    languageOptions: {
      globals: {
        ...globals.node,
      },
    },
  },
  // A handful of files deliberately co-locate a component with the plain
  // helper functions / hooks it needs, and export both — the helpers are
  // exercised directly by unit tests (see each file's own "exported for
  // tests" header comment) rather than only through the component. Splitting
  // them into a second file per component would add indirection with no
  // Fast-Refresh benefit here (these screens don't hot-reload in a loop the
  // way app views do), so the specific non-component export names are
  // allow-listed instead of disabling the rule file-wide.
  {
    files: ['src/builtin/drive/Drive.{jsx,tsx}'],
    rules: {
      'react-refresh/only-export-components': ['error', {
        allowExportNames: ['resumableUpload'],
      }],
    },
  },
])
