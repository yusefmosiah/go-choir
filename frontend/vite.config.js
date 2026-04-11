import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist',
  },
  server: {
    proxy: {
      // Proxy /auth/* to the auth service so the Playwright harness and
      // the dev frontend can call same-origin /auth/* routes without
      // hitting direct service ports.
      '/auth': {
        target: 'http://127.0.0.1:8081',
        changeOrigin: true,
      },
    },
  },
});
