import { defineConfig, type Plugin } from 'vite';
import { resolve } from 'node:path';

/**
 * In dev, strip the Office.js CDN tag.
 *
 * Office.js initializes itself on load and, outside a real Outlook client, wipes
 * `Office.context` - which would clobber whatever the dev harness installed. The
 * production HTML keeps the tag untouched; only the dev server sees it removed,
 * and src/dev-mock.ts supplies the API surface instead.
 */
function stripOfficeJsInDev(): Plugin {
  return {
    name: 'strip-office-js-in-dev',
    apply: 'serve',
    transformIndexHtml(html) {
      return html.replace(
        /\s*<script src="https:\/\/appsforoffice\.microsoft\.com[^>]*><\/script>/,
        '',
      );
    },
  };
}

// The add-in is a static site: no server, no API routes. Everything below is
// build-time only. See README for the Cloudflare Pages deploy.
export default defineConfig({
  plugins: [stripOfficeJsInDev()],
  root: resolve(import.meta.dirname, 'src'),
  publicDir: resolve(import.meta.dirname, 'assets'),
  build: {
    outDir: resolve(import.meta.dirname, 'dist'),
    emptyOutDir: true,
    rollupOptions: {
      input: {
        taskpane: resolve(import.meta.dirname, 'src/taskpane.html'),
      },
    },
  },
  server: {
    // Outlook requires HTTPS for every manifest URL. `npm run dev` is for
    // fast iteration on the UI in a plain browser; sideloading always points
    // at the deployed Pages URL.
    port: 3000,
  },
});
