/**
 * A Node resolution hook that maps an extensionless relative import to its .ts
 * file, so `node` can run the shell's TypeScript modules directly.
 *
 * Node strips TypeScript types natively (22.18+ / 23.6+) but does NOT do
 * extension resolution: `import './types'` from a .ts file is ERR_MODULE_NOT_FOUND
 * unless something maps it. Vite and tsc both resolve it (moduleResolution:
 * "Bundler"), so the source is correct as written — this hook exists purely so
 * the pack validator can be a build-free `node` invocation.
 *
 * Why this instead of writing `./types.ts` in the source: tsconfig.json sets
 * allowImportingTsExtensions: false, so an explicit .ts extension in an import
 * is a type error across the whole frontend. A 12-line dev-only hook is cheaper
 * than changing the project's module resolution to suit one CLI.
 */
export function resolve(specifier, context, next) {
  if (/^\.{1,2}\//.test(specifier) && !/\.([cm]?[jt]sx?|json)$/.test(specifier)) {
    return next(specifier + '.ts', context)
  }
  return next(specifier, context)
}
