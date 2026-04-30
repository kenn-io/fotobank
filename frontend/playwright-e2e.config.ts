import { defineConfig, devices } from "@playwright/test";

// Port 8080 is heavily contested in dev environments (Tomcat default,
// Jenkins, many other web servers); 18080 is rarely held by anything
// else. FOTOBANK_E2E_PORT lets a developer override at the shell.
// Both this config and cmd/e2e-server/main.go read the same env var
// so they stay aligned.
const port = Number(process.env.FOTOBANK_E2E_PORT ?? 18080);

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: "retain-on-failure",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
  webServer: {
    command: "../tmp/e2e-server",
    env: {
      FOTOBANK_E2E_PORT: String(port),
      // Shrink the hidden-auth lockout window/duration so the lockout
      // test in hidden.spec.ts resolves quickly AND the lockout state
      // doesn't leak into later specs (workers=1 runs them sequentially).
      // 2s is the smallest value that reliably accommodates 5 wrong-
      // passcode submissions on chromium (each fill+click+error-copy
      // round-trip stays under 400ms locally and on CI) while still
      // letting the lockout expire between specs. Subsequent unlock
      // attempts in lightbox.spec.ts also retry through any leftover
      // lockout (see the unlockHidden helper there) for belt-and-braces.
      FOTOBANK_E2E_LOCKOUT_WINDOW: "2s",
    },
    port,
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
