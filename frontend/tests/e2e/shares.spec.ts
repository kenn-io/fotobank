import { test, expect } from "@playwright/test";

test.describe("F2.3 owner-side sharing", () => {
  test("create media-set share from MediaDetail, see in /shares, revoke", async ({
    page,
  }) => {
    await page.goto("/media/gps-fixture-1");
    await expect(page.getByText("fotobank")).toBeVisible();
    await page.getByRole("button", { name: "Share" }).click();
    await page.getByPlaceholder("myhub:bob").fill("noop:test-grantee");
    await page.getByLabel("Label").fill("Test share");
    await page.getByRole("button", { name: "Create share" }).click();

    // ShareModal closes itself on success; navigate to /shares to
    // confirm the row landed and rendered with a state pill.
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page.goto("/shares");
    await expect(page.getByText("Test share")).toBeVisible();
    await expect(page.locator(".pill").first()).toBeVisible();

    // Revoke. Two "Revoke" buttons exist after click — the row's button
    // and the ConfirmModal's confirm. Scope the row click to the row,
    // and the confirm click to the dialog.
    const row = page.locator("tr", { has: page.getByText("Test share") });
    await row.getByRole("button", { name: "Revoke" }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Revoke" })
      .click();
    // After revoke, showRevoked stays false so the row drops out of
    // the visible list (refetchListPreservingFilter re-fetches with
    // include_settled=false). The ConfirmModal closes regardless.
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("seeded active share appears in /shares with state pill", async ({
    page,
  }) => {
    await page.goto("/shares");
    await expect(page.getByText("Active e2e share")).toBeVisible();
    // The seeded share is created at e2e-server boot. The NoopBroker
    // promotes pending → active asynchronously via the outbox worker;
    // either state surfaces a state pill, which is what we assert.
    await expect(page.locator(".pill").first()).toBeVisible();
  });

  test("share drawer opens on row click", async ({ page }) => {
    await page.goto("/shares");
    await expect(page.getByText("Active e2e share")).toBeVisible();
    // Clicking the row body opens the ShareDrawer (action cell stops
    // propagation so Revoke/Retry don't trigger the open).
    const row = page.locator("tr", {
      has: page.getByText("Active e2e share"),
    });
    await row.click();
    await expect(page.locator(".drawer")).toBeVisible();
    await expect(
      page.locator(".drawer dt", { hasText: "Grantee" }),
    ).toBeVisible();
  });

  test("delete-album-with-active-share toast deep links to /shares?album_id", async ({
    page,
  }) => {
    // Use the seeded "E2E Italy 2025" album. Share it via album_live,
    // then attempt to delete — the API returns 409 share.ErrAlbumHasLiveScopes
    // and AlbumDetail surfaces a toast with a /shares?album_id deep link.
    await page.goto("/albums");
    await expect(page.getByText("E2E Italy 2025")).toBeVisible();
    await page.getByText("E2E Italy 2025").click();
    await page.getByRole("button", { name: "Share album" }).click();
    await page.getByPlaceholder("myhub:bob").fill("noop:conflict");
    await page.getByRole("button", { name: "Create share" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);

    // Delete attempt → 409 → conflict toast with deep link.
    await page.getByRole("button", { name: "Delete" }).first().click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Delete" })
      .click();
    await expect(page.getByText(/active shares/i)).toBeVisible();
    await page.getByRole("link", { name: /view shares/i }).click();
    await expect(page).toHaveURL(/\/shares\?album_id=/);
    await expect(page.getByText(/Showing shares for album/)).toBeVisible();
  });
});
