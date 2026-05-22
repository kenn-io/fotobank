import { svelte } from "@sveltejs/vite-plugin-svelte";
import { svelteTesting } from "@testing-library/svelte/vite";
import type { UserConfig } from "vite";
import type { InlineConfig } from "vitest/node";

const apiUrl = process.env.FOTOBANK_DEV_API_URL ?? "http://127.0.0.1:8080";

// svelteTesting() adds the `browser` resolve condition under VITEST so
// Svelte 5's client-mode `mount(...)` is reachable from jsdom tests.
// Without it, @testing-library/svelte hits Svelte's SSR build and
// fails with "mount(...) is not available on the server".
const config = {
  base: "/",
  plugins: [svelte(), svelteTesting()],
  server: {
    host: "127.0.0.1",
    port: 5181,
    proxy: {
      "/api": { target: apiUrl, changeOrigin: true, ws: true },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.{test,spec}.?(c|m)[jt]s?(x)"],
    exclude: ["tests/e2e/**", "node_modules/**"],
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
} satisfies UserConfig & { test: InlineConfig };

export default config;
