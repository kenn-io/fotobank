import { test, expect } from "@playwright/test";

test.describe("F2.3 albums", () => {
  test("create album, add 2 photos, switch sort, remove 1, delete", async ({
    page,
  }) => {
    await page.goto("/albums");
    await expect(page.getByRole("heading", { name: "Albums" })).toBeVisible();

    // Create a fresh album. Cobra: AlbumsIndex's "+ New Album" button
    // opens the new-album modal, NewAlbumForm submits via api.POST.
    await page.getByRole("button", { name: "+ New Album" }).click();
    await page.getByLabel("Name").fill("Trip 2026");
    await page.getByRole("button", { name: "Create" }).click();
    await expect(page.getByText("Trip 2026")).toBeVisible();

    // Multi-select 2 photos in Library, then bulk Add to album. The
    // Library list is async — wait for at least one fixture cell to
    // hydrate before the modifier-clicks; otherwise the race against
    // MediaStore.loadInitial() can swallow the first click.
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    await page
      .getByLabel("Photo gps-fixture-1")
      .click({ modifiers: ["Meta"] });
    await page
      .getByLabel("Photo pair-fixture-primary")
      .click({ modifiers: ["Meta"] });
    await expect(page.getByText("2 selected")).toBeVisible();
    await page.getByRole("button", { name: "Add to album" }).click();
    // Pick the freshly created album in the modal list, then submit.
    await page.getByRole("dialog").getByText("Trip 2026").click();
    await page.getByRole("button", { name: "Add 2 photos" }).click();

    // Open the album and verify both members are present.
    await page.goto("/albums");
    await page.getByText("Trip 2026").click();
    await expect(page).toHaveURL(/\/albums\/[a-f0-9-]+$/);
    await expect(
      page.getByLabel(/^Photo (gps-fixture-1|pair-fixture-primary)$/),
    ).toHaveCount(2);

    // Switch sort to Recently added; the store refetches with sort_by=added.
    await page.getByLabel("Sort:").selectOption("added");
    await expect(page.getByLabel(/^Photo /)).toHaveCount(2);

    // Multi-select 1 photo, click Remove from this album. AlbumDetail
    // bulks via the route-scoped selectedInAlbum derivation — the
    // single-id click puts a 1-photo selection into the action bar.
    await page
      .getByLabel("Photo gps-fixture-1")
      .click({ modifiers: ["Meta"] });
    await expect(page.getByText("1 selected")).toBeVisible();
    await page
      .getByRole("button", { name: "Remove from this album" })
      .click();
    await expect(page.getByLabel(/^Photo /)).toHaveCount(1);

    // Delete the album. Two "Delete" buttons exist after the confirm
    // modal opens (header + ConfirmModal); .first() targets the header,
    // then scope the confirmation click to the dialog.
    await page.getByRole("button", { name: "Delete" }).first().click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Delete" })
      .click();
    await expect(page).toHaveURL(/\/albums$/);
    // AlbumsStore caches the prior list (#359 follow-up); a SPA-only
    // navigation after delete still shows the cached tile. A full
    // reload forces /api/v1/albums to refetch, which is the only way
    // to verify the row was actually deleted server-side.
    await page.reload();
    await expect(page.getByText("Trip 2026")).not.toBeVisible();
  });

  test("bulk-select-by-group via month header", async ({ page }) => {
    await page.goto("/library");
    await expect(page.getByLabel(/^Photo /).first()).toBeVisible();
    // GroupSelectButton sits inside MonthChunk's .day-header. The
    // aria-label is `Select <N> photos in <label>` — clicking it adds
    // the entire month's ids to the global selection store.
    const firstHeader = page.locator(".day-header").first();
    await expect(firstHeader).toBeVisible();
    await firstHeader
      .getByRole("button", { name: /^Select \d+ photos in/ })
      .click();
    await expect(page.getByText(/\d+ selected/)).toBeVisible();
  });

  test("MediaActions hidden on sidecar direct page", async ({ page }) => {
    // pair-fixture-sidecar has paired_with_id set, so MediaDetail
    // takes the sidecar branch and renders no MediaActions header
    // (Add/Share buttons are gated on the primary branch).
    await page.goto("/media/pair-fixture-sidecar");
    await expect(page.getByText(/RAW sidecar for/)).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Add to album" }),
    ).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Share" })).toHaveCount(0);
  });
});
