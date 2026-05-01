import { defineConfig, devices } from "@playwright/test";

// Port 8080 is heavily contested in dev environments (Tomcat default,
// Jenkins, many other web servers); 18080 is rarely held by anything
// else. FOTOBANK_E2E_PORT lets a developer override at the shell.
// Both this config and cmd/e2e-server/main.go read the same env var
// so they stay aligned. A blank or non-integer override falls back to
// 18080 rather than coercing to NaN/0 (which would make Playwright
// target http://127.0.0.1:0).
function resolvePort(): number {
  const raw = process.env.FOTOBANK_E2E_PORT;
  if (raw === undefined || raw.trim() === "") return 18080;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1 || n > 65535) {
    throw new Error(
      `FOTOBANK_E2E_PORT must be an integer in [1, 65535] (got ${JSON.stringify(raw)})`,
    );
  }
  return n;
}

const port = resolvePort();

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
      // FOTOBANK_E2E_MODE=1 lets internal/cli/server.go honor the
      // window override below. Without it the override is silently
      // ignored — see internal/cli/server.go for the rationale.
      FOTOBANK_E2E_MODE: "1",
      // Shrink the hidden-auth lockout window/duration so the lockout
      // test in hidden.spec.ts resolves quickly AND the lockout state
      // doesn't leak into later specs (workers=1 runs them sequentially).
      // 5s gives slow CI runners enough time to land 5 wrong-passcode
      // submissions inside the failure-counting window — 2s was racy on
      // CI when each fill+click+error round-trip drifted past 400ms.
      // The lockout expiry is the same value, which is acceptable here
      // because subsequent specs' unlockHidden helper retries through
      // any leftover lockout state.
      FOTOBANK_E2E_LOCKOUT_WINDOW: "5s",
    },
    port,
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
