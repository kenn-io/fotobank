// SF-20 — sidebar facet end-to-end coverage. The mount-only smoke
// (FILTERS group renders + Places hidden on /map) lives in
// filters-sidebar.spec.ts; this file exercises the toggle/clear/
// composition flows against the facet-fixture-* seeds in
// cmd/e2e-server/main.go.
import { test, expect, type Page } from "@playwright/test";

// Each FacetList row is rendered as <button role="checkbox" aria-label="<label>">,
// so the most stable selector is by role + accessible name. The
// AppHeader doesn't surface a "Sony A7R IV" string anywhere so this
// is unambiguous across the page.
async function clickCameraFacet(page: Page, label: string): Promise<void> {
  await page.getByRole("checkbox", { name: label }).click();
}

async function clickTagFacet(page: Page, label: string): Promise<void> {
  // Tag rows share the FacetList shell with cameras/lenses; the
  // accessible name is the tag label (which equals the key for the
  // facet-fixture-* seeds — both pass through writeSearchAIResults
  // with key=label=keyword).
  await page.getByRole("checkbox", { name: label }).click();
}

test.describe("SF-20 sidebar facets — toggle/clear/persistence", () => {
  test("Camera facet narrows the library and renders a chip", async ({ page }) => {
    await page.goto("/library");
    // Wait for the facet section to hydrate — the FilterSidebar reads
    // /api/v1/facets via facetsStore, and clicking before the row
    // mounts would silently miss the click target.
    await expect(page.getByRole("checkbox", { name: "Sony A7R IV" })).toBeVisible();

    await clickCameraFacet(page, "Sony A7R IV");

    // URL gains the camera param (URLSearchParams encodes spaces as +
    // when stringified inside withFilters; confirm the param is set
    // regardless of whether the SPA used + or %20 by reading the
    // parsed search params rather than matching the raw URL string).
    await expect.poll(() => new URL(page.url()).searchParams.getAll("camera"))
      .toEqual(["Sony A7R IV"]);

    // Chip strip surfaces the selection with the canonical label.
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toBeVisible();

    // Grid still renders at least one tile — chip + URL update is the
    // smoke signal; we don't pin an exact count because the seeded
    // Sony rows land alongside other fixtures and the grid response
    // shape is covered by mediaStore tests.
    await expect(page.locator("[data-media-id]").first()).toBeVisible();
  });

  test("Clicking the chip removes the filter", async ({ page }) => {
    await page.goto("/library?camera=Sony+A7R+IV");
    const chip = page.locator(".chip", { hasText: "Sony A7R IV" });
    await expect(chip).toBeVisible();

    // The close affordance (.chip-x) is aria-hidden; the click handler
    // is on the parent <button class="chip">, so click that.
    await chip.click();

    await expect.poll(() => new URL(page.url()).searchParams.has("camera")).toBe(false);
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toHaveCount(0);
  });

  test("Filter survives /library → lightbox → back navigation", async ({ page }) => {
    await page.goto("/library?camera=Sony+A7R+IV");
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toBeVisible();
    await expect(page.locator("[data-media-id]").first()).toBeVisible();

    // Open the lightbox from the first thumbnail. The grid click
    // navigates to /media/:id?from=library; we don't care which row
    // wins so we just confirm the lightbox URL pattern.
    await page.locator("[data-media-id]").first().click();
    await expect(page).toHaveURL(/\/media\/.+\?from=library/);

    await page.goBack();
    await expect(page).toHaveURL(/\/library\?/);
    await expect.poll(() => new URL(page.url()).searchParams.getAll("camera"))
      .toEqual(["Sony A7R IV"]);
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toBeVisible();
  });

  test("Tag facet narrows the library and renders a chip", async ({ page }) => {
    await page.goto("/library");
    // The tag facet uses facet_tag (NOT the search "tag" key) — see
    // activeFilters.ts. Wait for the row to render before clicking.
    await expect(page.getByRole("checkbox", { name: "dog" })).toBeVisible();

    await clickTagFacet(page, "dog");

    await expect.poll(() => new URL(page.url()).searchParams.getAll("facet_tag"))
      .toEqual(["dog"]);
    await expect(page.locator(".chip", { hasText: /tag:\s*dog/i })).toBeVisible();
  });

  test("Two filters compose as AND (camera + tag)", async ({ page }) => {
    // Direct-entry with both filters; the grid AND-composes them.
    // Sony A7R IV ∩ tag=dog → exactly 2 rows: facet-fixture-sony-1 and
    // facet-fixture-sony-2. (sony-3 is Sony A7R IV but tagged 'cat';
    // canon-1 is dog-tagged but Canon EOS R5; iphone fixtures have no
    // dog tag.) The exact-set assertion catches an OR regression — an
    // AND-as-OR bug would surface canon-1 / sony-3 in the grid.
    await page.goto("/library?camera=Sony+A7R+IV&facet_tag=dog");

    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toBeVisible();
    await expect(page.locator(".chip", { hasText: /tag:\s*dog/i })).toBeVisible();
    // Two chips so the strip's "Clear all" affordance is reachable.
    await expect(page.locator(".clear-all")).toBeVisible();

    // Wait for the grid to render before snapshotting media IDs —
    // otherwise the .all() can race the initial paint and return an
    // empty list.
    await expect(page.locator("[data-media-id]").first()).toBeVisible();

    // Snapshot the rendered media IDs and verify the grid contains
    // exactly the two AND-intersection rows. Order is sort-determined
    // (newest-first; ImportedAt is descending across the facet
    // fixtures, so sony-1 is newer than sony-2), but the assertion is
    // membership-style to stay tolerant of unrelated sort tweaks.
    const ids = await page
      .locator("[data-media-id]")
      .evaluateAll((els) => els.map((el) => el.getAttribute("data-media-id")));
    expect(ids.sort()).toEqual(["facet-fixture-sony-1", "facet-fixture-sony-2"]);
  });
});
