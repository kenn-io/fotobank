import { defineConfig, devices } from "@playwright/test";

// Companion config for the /library Playwright scale suite. Boots a
// separate e2e-server with FOTOBANK_E2E_SCALE_ROWS=N so the seed
// replaces the curated fixture set with a bulk mediaseed.SeedScaleLibrary
// call. No thumb files are written; /thumb 404s for every cell, the
// SPA renders the broken-image placeholder, and the request count we
// measure includes those 404s — that's the realistic shape.
//
// Usage:
//   bun run test:e2e:scale
//
// The seed is large (100k rows). The webServer.timeout is bumped to
// 120s so the SeedScaleLibrary call (a few seconds at this scale on
// a fast machine, longer on CI) doesn't time out the boot.

function resolvePort(): number {
  const raw = process.env.FOTOBANK_E2E_SCALE_PORT;
  if (raw === undefined || raw.trim() === "") return 18082;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1 || n > 65535) {
    throw new Error(
      `FOTOBANK_E2E_SCALE_PORT must be in [1, 65535] (got ${JSON.stringify(raw)})`,
    );
  }
  return n;
}

function resolveScaleRows(): number {
  const raw = process.env.FOTOBANK_E2E_SCALE_ROWS;
  if (raw === undefined || raw.trim() === "") return 100_000;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1) {
    throw new Error(
      `FOTOBANK_E2E_SCALE_ROWS must be a positive integer (got ${JSON.stringify(raw)})`,
    );
  }
  return n;
}

// Real-thumbs mode (PS-3f) writes a tiny grid JPEG for every seeded
// row so img.decode work is present in the browser. Without this the
// SPA renders broken-image placeholders for every cell and there's no
// decode work for content-visibility:auto to defer. Off by default —
// the daily scale spec cares about DOM/request shape, not pixel
// decode. Set to "1" or "true" to opt in.
function resolveRealThumbs(): string {
  const raw = process.env.FOTOBANK_E2E_SCALE_REAL_THUMBS ?? "";
  return raw === "1" || raw === "true" ? "1" : "";
}

const port = resolvePort();
const scaleRows = resolveScaleRows();
const realThumbs = resolveRealThumbs();

export default defineConfig({
  testDir: "./tests/e2e/scale",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: [["list"]],
  // Each spec captures DOM/heap snapshots and pages through 10 fetches
  // — generous timeout so a slow boot or a sluggish CI runner doesn't
  // false-positive a perf regression.
  timeout: 120_000,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "../tmp/e2e-server",
    env: {
      FOTOBANK_E2E_PORT: String(port),
      FOTOBANK_E2E_MODE: "1",
      FOTOBANK_E2E_LOCKOUT_WINDOW: "5s",
      FOTOBANK_E2E_SCALE_ROWS: String(scaleRows),
      FOTOBANK_E2E_SCALE_REAL_THUMBS: realThumbs,
    },
    port,
    reuseExistingServer: false,
    // Bumped from 60s default because the 100k-row seed takes a few
    // seconds locally and longer on CI runners. Empirical: a fresh
    // SeedScaleLibrary at 100k completes in 2-3s on M5 Max, ~10-15s
    // on a CI Linux runner. 120s leaves ample headroom.
    timeout: 120_000,
  },
});
