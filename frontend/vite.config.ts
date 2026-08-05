/// <reference types="vitest/config" />
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],
  // Wails serves the built assets from an embedded FS at the app root.
  base: './',
  build: {
    // Wails' //go:embed all:frontend/dist expects the output here.
    outDir: 'dist',
    emptyOutDir: true,
    target: 'esnext',
  },
  // Vitest. The suite pins the pure, ported logic — the `<script module>` blocks
  // and api.ts — against answers produced by running app.py's own slices under
  // CPython (src/lib/__tests__/app-py-oracle.json). It renders nothing: every
  // subject is a module-context export, so no DOM and no component harness.
  test: {
    // No jsdom. Vitest transforms these files through Vite's SSR pipeline, so
    // vite-plugin-svelte compiles the components for the server and the module
    // blocks are reachable without a document. Adding an environment would only
    // add a dependency and hide the fact that nothing here needs one.
    environment: 'node',
    include: ['src/**/*.test.ts'],
    alias: [
      {
        // HARD RULE: no test may reach the Wails bridge. `src/lib/api.ts`
        // re-exports the generated bindings, and importing it for real would
        // pull in `wailsjs/go/ui/App.js`, whose every function is a call into
        // `window.go.ui.App.*` — the same door StopAll and the money-spending
        // job starters go through. This alias makes the real module
        // unreachable from a test run and substitutes one whose exports all
        // throw. It lives under `test` only, so `vite build` is untouched.
        // Whole-specifier match: Vite's alias replaces only the matched slice,
        // so a partial pattern would splice the absolute path onto the leading
        // `../..` and resolve to nothing.
        find: /^.*\/wailsjs\/go\/ui\/App$/,
        replacement: fileURLToPath(new URL('./src/lib/__tests__/wails-bridge-refusal.ts', import.meta.url)),
      },
    ],
  },
})
