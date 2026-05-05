import { test, expect } from "@playwright/test";

test.describe("F2.3 owner-side sharing", () => {
  test("create media-set share from MediaDetail, see in /shares, revoke", async ({
    page,
  }) => {
    // Unique-per-run label so a retry after a partial failure (share
    // created, test failed before revoke) doesn't see two rows match
    // "Test share". CI has retries enabled.
    const shareLabel = `Test share ${Date.now()}`;
    await page.goto("/media/gps-fixture-1");
    await expect(page.getByText("fotobank")).toBeVisible();
    await page.getByRole("button", { name: "Share" }).click();
    await page.getByPlaceholder("myhub:bob").fill("noop:test-grantee");
    await page.getByLabel("Label").fill(shareLabel);
    await page.getByRole("button", { name: "Create share" }).click();

    // ShareModal closes itself on success; navigate to /shares to
    // confirm the row landed and rendered with a state pill.
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page.goto("/shares");
    await expect(page.getByText(shareLabel)).toBeVisible();
    await expect(page.locator(".pill").first()).toBeVisible();

    // Revoke. Two "Revoke" buttons exist after click — the row's button
    // and the ConfirmModal's confirm. Scope the row click to the row,
    // and the confirm click to the dialog.
    const row = page.locator("tr", { has: page.getByText(shareLabel) });
    await row.getByRole("button", { name: "Revoke" }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Revoke" })
      .click();
    // The ConfirmModal closes after revoke succeeds. The row stays
    // visible with broker_status="revoking" until the (noop) broker
    // confirms, so include_settled=false doesn't drop it. The pill
    // transitions to "Revoking…" — that's the user-visible signal.
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(row.locator(".pill")).toContainText(/Revoking/);
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

  test("delete-album-with-active-share surfaces CLI-aware conflict toast", async ({
    page,
  }) => {
    // Use the seeded "E2E Italy 2025" album. The seed already creates an
    // album_live share against it (cmd/e2e-server/main.go:621), so a
    // delete attempt returns 409 share.ErrAlbumHasLiveScopes and
    // AlbumDetail surfaces the CLI-aware conflict toast. The earlier
    // SPA-aware variant (with a "View shares" link) was deliberately
    // dropped in commit 0882470 because the CLI works regardless of the
    // [ui].sharing_enabled flag — see the AlbumDetail unit test
    // ("references the CLI command, not the in-app /shares page").
    await page.goto("/albums");
    await expect(page.getByText("E2E Italy 2025")).toBeVisible();
    await page.getByText("E2E Italy 2025").click();

    // Delete attempt → 409 → CLI-aware conflict toast.
    await page.getByRole("button", { name: "Delete" }).first().click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Delete" })
      .click();
    const toast = page.getByRole("alert");
    await expect(toast).toBeVisible();
    await expect(toast).toContainText(/active CLI shares/i);
    await expect(toast).toContainText("fotobank shares list --album");
    // No SPA deep-link — that surface is intentionally CLI-only.
    await expect(toast.getByRole("link", { name: /view shares/i })).toHaveCount(
      0,
    );
  });

  test("shares page filters to a single album via ?album_id", async ({
    page,
  }) => {
    // The seeded album_live share against "E2E Italy 2025" lives under a
    // known album_id; navigating to /shares?album_id=<id> filters the
    // table to just that album's shares and surfaces the
    // "Showing shares for album <id>" header banner.
    await page.goto("/albums");
    await page.getByText("E2E Italy 2025").click();
    // AlbumDetail's URL is /albums/<id> — pull the id from window.location
    // rather than scraping it out of the toast (which no longer renders
    // the id) or the seed (which is non-deterministic).
    const albumId = await page.evaluate(() =>
      window.location.pathname.replace(/^\/albums\//, ""),
    );
    expect(albumId).toMatch(/^[0-9a-f-]{36}$/);
    await page.goto(`/shares?album_id=${albumId}`);
    await expect(page.getByText(/Showing shares for album/)).toBeVisible();
    await expect(page.getByText("Active album e2e share")).toBeVisible();
    // Negative: the seeded media-set share ("Active e2e share", target
    // gps-fixture-1) shows on /shares without the filter. If
    // ?album_id=... were silently ignored the row would still be here,
    // and the positive assertion alone would fail to catch it. Asserting
    // its absence pins the filter behavior.
    await expect(page.getByText("Active e2e share", { exact: true })).toHaveCount(0);
  });
});
