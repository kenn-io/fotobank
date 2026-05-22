import { defineConfig, devices } from "@playwright/test";

// Companion config for the sharing-disabled Playwright suite. Boots a
// separate e2e-server on a different port with FOTOBANK_E2E_SHARING_ENABLED=false
// so the SPA receives features.sharing_enabled=false from /api/v1/me and
// gates its sharing surfaces accordingly. The default suite uses
// playwright-e2e.config.ts (sharing UI on).
//
// Usage:
//   bun run test:e2e:sharing-disabled
//
// The two configs share the same testDir — testMatch on this config
// scopes execution to the single sharing-disabled spec, and the default
// config's testIgnore drops it from the broad suite. Both can run on
// the same developer machine because they listen on different ports.

function resolvePort(): number {
  const raw = process.env.FOTOBANK_E2E_SHARING_DISABLED_PORT;
  if (raw === undefined || raw.trim() === "") return 18081;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1 || n > 65535) {
    throw new Error(
      `FOTOBANK_E2E_SHARING_DISABLED_PORT must be in [1, 65535] (got ${JSON.stringify(raw)})`,
    );
  }
  return n;
}

const port = resolvePort();

export default defineConfig({
  testDir: "./tests/e2e",
  testMatch: ["sharing-disabled.spec.ts"],
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: [["list"]],
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
      // The single knob this config exists for — boots the SPA with
      // features.sharing_enabled=false so the gated UI paths render
      // without the share entries.
      FOTOBANK_E2E_SHARING_ENABLED: "false",
    },
    port,
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
