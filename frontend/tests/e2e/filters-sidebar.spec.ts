// Smoke coverage for the FilterSidebar mount (SF-16). The full
// suite — toggle/clear/cache behavior across /library, /search,
// /map — lands with SF-20. This file is the early gate that
// catches "FILTERS group never renders" regressions.
import { test, expect } from "@playwright/test";

test.describe("FilterSidebar mount", () => {
  test("renders FILTERS section on /library", async ({ page }) => {
    await page.goto("/library");
    await expect(page.getByText("FILTERS")).toBeVisible();
    await expect(page.getByText("Cameras")).toBeVisible();
    await expect(page.getByText("Lenses")).toBeVisible();
    await expect(page.getByText("Tags")).toBeVisible();
    await expect(page.getByText("Places")).toBeVisible();
    await expect(page.getByText("Media Type")).toBeVisible();
  });

  // /map is geotagged-only by definition, so the Places facet is
  // suppressed (the FilterSidebar conditions on `route !== "map"`).
  test("hides Places on /map", async ({ page }) => {
    await page.goto("/map");
    await expect(page.getByText("FILTERS")).toBeVisible();
    await expect(page.getByText("Cameras")).toBeVisible();
    await expect(page.getByText("Places")).toHaveCount(0);
  });

  test("absent on /albums", async ({ page }) => {
    await page.goto("/albums");
    await expect(page.getByText("FILTERS")).toHaveCount(0);
  });
});
