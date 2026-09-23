/// <reference types="vitest/config" />
// Vite build for the MangaVeyra frontend.
//
// Everything is emitted into internal/web/static/dist (embedded into the Go
// binary) with a manifest the Go asset resolver reads. The single entry is
// src/main.ts (HTMX glue + all CSS); islands are code-split chunks loaded on
// demand through src/islands/registry.ts. Nothing is ever loaded from a CDN.
import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { fileURLToPath } from 'node:url';

const outDir = fileURLToPath(new URL('../internal/web/static/dist', import.meta.url));

/**
 * woff2Only strips the woff/ttf fallbacks from third-party @font-face rules
 * (Tabler Icons) so only the woff2 files end up in the binary. Every browser
 * MangaVeyra supports reads woff2.
 */
function woff2Only(): Plugin {
  return {
    name: 'mv-woff2-only',
    enforce: 'pre',
    transform(code, id) {
      if (!/tabler-icons(\.min)?\.css/.test(id)) return null;
      return code.replace(/,\s*url\("[^"]+\.(?:woff|ttf)(?:\?[^"]*)?"\)\s*format\("(?:woff|truetype)"\)/g, '');
    },
  };
}

export default defineConfig({
  base: '/static/dist/',
  plugins: [woff2Only(), svelte()],
  publicDir: false,
  build: {
    outDir,
    emptyOutDir: true,
    manifest: 'manifest.json',
    target: 'es2022',
    cssCodeSplit: true,
    assetsInlineLimit: 0,
    sourcemap: false,
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      input: { main: fileURLToPath(new URL('./src/main.ts', import.meta.url)) },
      output: {
        // Islands get readable chunk names: assets/island-editor-<hash>.js.
        chunkFileNames(chunk) {
          const m = chunk.facadeModuleId?.match(/\/islands\/([^/]+)\/main\.ts$/);
          return m ? `assets/island-${m[1]}-[hash].js` : 'assets/[name]-[hash].js';
        },
      },
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
  },
});
