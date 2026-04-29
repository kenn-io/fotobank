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
    env: { FOTOBANK_E2E_PORT: String(port) },
    port,
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
