import { test, expect } from "@playwright/test";

test.describe("F2.3 albums", () => {
  test("create album, add 2 photos, switch sort, remove 1, delete", async ({
    page,
  }) => {
    // Unique-per-run name so a retry after a partial failure (album
    // created, test failed before delete) doesn't collide with the
    // leftover row on the next attempt. CI has retries enabled.
    const albumName = `Trip ${Date.now()}`;
    await page.goto("/albums");
    await expect(page.getByRole("heading", { name: "Albums" })).toBeVisible();

    // Create a fresh album. AlbumsIndex's "New album" button
    // opens the new-album modal, NewAlbumForm submits via api.POST.
    await page.getByRole("button", { name: "New album" }).click();
    await page.getByLabel("Name").fill(albumName);
    await page.getByRole("button", { name: "Create" }).click();
    await expect(page.getByText(albumName)).toBeVisible();

    // Multi-select 2 photos in Library, then bulk Add to album. The
    // Library list is async — wait for at least one fixture cell to
    // hydrate before the modifier-clicks; otherwise the race against
    // MediaStore.loadInitial() can swallow the first click.
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    await page.getByLabel("Photo gps-fixture-1").click({ modifiers: ["Meta"] });
    await page
      .getByLabel("Photo pair-fixture-primary")
      .click({ modifiers: ["Meta"] });
    await expect(page.getByText("2 selected")).toBeVisible();
    await page.getByRole("button", { name: "Add to album" }).click();
    // Pick the freshly created album in the modal list, then submit.
    await page.getByRole("dialog").getByText(albumName).click();
    await page.getByRole("button", { name: "Add 2 photos" }).click();
    // Wait for the modal to close — onAdd POSTs, then AddToAlbumModal
    // calls onClose() on success. Without this wait the next page.goto
    // can race the in-flight POST and produce flakes.
    await expect(page.getByRole("dialog")).toHaveCount(0);

    // Open the album and verify both members are present.
    await page.goto("/albums");
    await page.getByText(albumName).click();
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
    await page.getByLabel("Photo gps-fixture-1").click({ modifiers: ["Meta"] });
    await expect(page.getByText("1 selected")).toBeVisible();
    await page.getByRole("button", { name: "Remove from this album" }).click();
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
    await expect(page.getByText(albumName)).not.toBeVisible();

    // Reload so the next /albums fetch comes from the server, not the
    // SPA cache. The server-side delete must really have happened —
    // otherwise the album would reappear after a refetch. Wait for the
    // /api/v1/albums response before asserting so we don't race the
    // SPA's async load (a UI assertion alone could pass against an
    // empty pre-populated grid).
    const refetched = page.waitForResponse(
      (resp) => resp.url().includes("/api/v1/albums") && resp.status() === 200,
    );
    await page.reload();
    const resp = await refetched;
    const body = (await resp.json()) as { items?: Array<{ name?: string }> };
    const names = (body.items ?? []).map((it) => it.name);
    expect(names).not.toContain(albumName);
    await expect(page.getByText(albumName)).not.toBeVisible();
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
});
