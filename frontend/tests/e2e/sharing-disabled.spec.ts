import { test, expect, type Page } from "@playwright/test";

// This suite expects the e2e-server to be started with
// FOTOBANK_E2E_SHARING_ENABLED=false (see playwright-e2e-sharing-disabled.config.ts
// for the wiring). The default e2e suite runs against the
// sharing-enabled config; this one validates the gated SPA paths.

// Goto + assert the SPA actually received features.sharing_enabled=false
// from the server before we check that gated UI is absent. The SPA
// defaults appConfig.sharingEnabled to false while the /api/v1/me
// request is pending, so a naive absence assertion would falsely pass
// even if the server regressed to sharing-enabled or never responded.
// Watching the response body proves both that the API actually
// answered AND that the value is the one this suite is meant to test.
async function gotoAndAssertSharingDisabled(
  page: Page,
  path: string,
): Promise<void> {
  const responsePromise = page.waitForResponse(
    (r) => r.url().includes("/api/v1/me") && r.status() === 200,
  );
  await page.goto(path);
  const response = await responsePromise;
  const body = (await response.json()) as {
    features?: { sharing_enabled?: boolean };
  };
  expect(body.features?.sharing_enabled).toBe(false);
}

test.describe("Sharing UI flag-gate (disabled)", () => {
  test("Sidebar has no Shares entry", async ({ page }) => {
    await gotoAndAssertSharingDisabled(page, "/");
    await expect(
      page.getByRole("link", { name: /^shares$/i }),
    ).toHaveCount(0);
  });

  test("MediaDetail has no Share button", async ({ page }) => {
    // Navigate to a known seeded media row. With sharing disabled, the
    // MediaActions row drops the Share button entirely.
    await gotoAndAssertSharingDisabled(page, "/media/gps-fixture-1");
    await expect(page.getByText("fotobank")).toBeVisible();
    await expect(
      page.getByRole("button", { name: /^share$/i }),
    ).toHaveCount(0);
  });

  test("AlbumDetail has no Share album button", async ({ page }) => {
    // Open the seeded "E2E Italy 2025" album. The disabled gate hides
    // the Share album header button.
    await gotoAndAssertSharingDisabled(page, "/albums");
    await expect(
      page.getByRole("heading", { name: "Albums" }),
    ).toBeVisible();
    await page.getByText("E2E Italy 2025").click();
    await expect(page).toHaveURL(/\/albums\/[a-f0-9-]+$/);
    await expect(
      page.getByRole("button", { name: /share album/i }),
    ).toHaveCount(0);
  });

  test("/shares redirects to /", async ({ page }) => {
    await gotoAndAssertSharingDisabled(page, "/shares");
    // App.svelte's route guard navigates with replace:true, so the URL
    // settles at "/" (or its alias /library). Either is acceptable —
    // assert we landed away from /shares.
    await expect(page).not.toHaveURL(/\/shares$/);
  });

  test("Album with active CLI shares surfaces CLI-aware delete copy", async ({
    page,
  }) => {
    // The seed creates a TargetAlbumLive share over "E2E Italy 2025"
    // (cmd/e2e-server/main.go::seedFixtures). With the share active,
    // album delete returns 409 and the SPA must surface the CLI command
    // — the "View shares" / "Revoke them in Shares first" copy is gone.
    await gotoAndAssertSharingDisabled(page, "/albums");
    await page.getByText("E2E Italy 2025").click();
    await expect(page).toHaveURL(/\/albums\/[a-f0-9-]+$/);
    // Album header has a Delete button. Clicking it opens ConfirmModal;
    // the modal's Confirm button is also labeled Delete and styled
    // .danger — pick that one.
    await page
      .getByRole("button", { name: "Delete" })
      .first()
      .click();
    await page
      .getByRole("dialog")
      .locator("button.danger")
      .click();
    await expect(page.getByText(/active CLI shares/i)).toBeVisible();
    await expect(
      page.getByText(/fotobank shares list --album/i),
    ).toBeVisible();
    // The old "View shares" / "Revoke them in Shares first" copy is gone.
    await expect(page.getByText(/View shares/i)).toHaveCount(0);
    await expect(page.getByText(/Revoke them in Shares first/i)).toHaveCount(0);
  });
});
