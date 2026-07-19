import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  // `dist` is the built SPA (committed — see repo .gitignore, it's embedded via
  // //go:embed into the Go binary) and not source we lint.
  globalIgnores(['dist', 'node_modules']),
  {
    files: ['**/*.{js,jsx}'],
    extends: [
      js.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
      parserOptions: {
        ecmaVersion: 'latest',
        ecmaFeatures: { jsx: true },
        sourceType: 'module',
      },
    },
    rules: {
      // Pragmatic baseline, not maximal strictness: this is the FIRST time
      // lint has ever run against this console (see CHANGELOG). Allow
      // intentionally-unused args/rest-siblings (common in handler
      // signatures and destructuring) without silencing genuinely dead
      // top-level bindings/imports.
      'no-unused-vars': [
        'error',
        { args: 'none', varsIgnorePattern: '^[A-Z_]', ignoreRestSiblings: true },
      ],
      // react-hooks v7's "recommended" flags any setState called directly in
      // an effect body (incl. the common "reset loading/error state before a
      // fetch" idiom used throughout this console's admin pages). It's a
      // real perf/style rule worth surfacing, but not a correctness bug — kept
      // as a warning (visible, non-blocking) rather than silenced or treated
      // as a hard error on a first-ever lint pass. See CHANGELOG baseline count.
      'react-hooks/set-state-in-effect': 'warn',
      // Flags files that export non-component bindings alongside components
      // (breaks Vite Fast Refresh for that file only). console/icons.jsx is a
      // legitimate icon-name -> renderer lookup table, not a component module
      // — downgraded to a warning rather than restructured.
      'react-refresh/only-export-components': 'warn',
    },
  },
  // Node-context config files.
  {
    files: ['vite.config.js', 'eslint.config.js'],
    languageOptions: {
      globals: { ...globals.node },
    },
  },
])
