import { test, expect } from "@playwright/test";

// Tile availability is NOT asserted — OSM tiles are best-effort over
// the public network and would make this suite flaky. We assert SPA
// structure, route state, attribution presence, and grid/lightbox
// behavior. Tile rendering itself is a manual check.
//
// Seed fixtures (cmd/e2e-server/main.go::seedFixtures):
//   gps-fixture-1   Paris    (48.8566, 2.3522)
//   geo-photo-a     SF       (37.7749, -122.4194)
//   geo-photo-b     SF       (37.7750, -122.4195)
//   geo-photo-c     NYC      (40.7128, -74.0060)
//
// Four geotagged rows total; geo-photo-a/b cluster at low zoom.

test.describe("/map page", () => {
  test("loads, shows attribution, and renders a marker layer", async ({ page }) => {
    await page.goto("/map");
    await expect(page.getByTestId("map-pane")).toBeVisible();
    await expect(page.locator(".leaflet-control-attribution")).toContainText(
      "OpenStreetMap",
    );
    await expect(page.locator(".leaflet-marker-pane")).toBeAttached();
  });

  test("clicking a marker opens the lightbox with from=map", async ({ page }) => {
    // Zoom into SF so the geo-photo-a/b cluster expands into individual
    // markers we can click. Zoom 12 is past the markercluster threshold
    // for two points within ~50m of each other.
    await page.goto("/map?z=12&c=37.7749,-122.4194");
    await expect(page.getByTestId("map-pane")).toBeVisible();
    await page.locator(".leaflet-marker-icon").first().click();
    await expect(page).toHaveURL(/\/media\/[^?]+\?from=map/);
    await expect(page.getByTestId("lightbox")).toBeVisible();
  });

  test("lightbox map pin navigates to /map?focus=<id>", async ({ page }) => {
    // Seed gps-fixture-1 has GPS — its lightbox shows the map pin.
    await page.goto("/media/gps-fixture-1?from=library");
    await expect(page.getByTestId("lightbox-map-pin")).toBeVisible();
    await page.getByTestId("lightbox-map-pin").click();
    await expect(page).toHaveURL(/\/map\?z=14&c=[^&]+&focus=gps-fixture-1/);
  });

  test("hidden toggle is absent when locked", async ({ page }) => {
    await page.goto("/map");
    await expect(page.getByTestId("map-pane")).toBeVisible();
    await expect(page.getByLabel(/include hidden/i)).toHaveCount(0);
  });

  test("mobile viewport renders tabs and switches between map and photos", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 600, height: 900 });
    await page.goto("/map");
    await expect(page.getByTestId("map-pane")).toBeVisible();
    const mapTab = page.getByRole("button", { name: /^map$/i });
    const photosTab = page.getByRole("button", { name: /^photos$/i });
    await expect(mapTab).toBeVisible();
    await expect(photosTab).toBeVisible();
    await photosTab.click();
    await expect(photosTab).toHaveClass(/active/);
    await mapTab.click();
    await expect(mapTab).toHaveClass(/active/);
  });
});
