import { test, expect, type Page } from "@playwright/test";

// ---------------------------------------------------------------------------
// Scroll helpers — the app's scroll container is `.main` (the overflow:auto
// section in ThreeColumnLayout), NOT `window`. Reading window.scrollY would
// always return 0 and silently pass the scroll-restore assertions.
// ---------------------------------------------------------------------------
async function getMainScrollTop(page: Page): Promise<number> {
  return page.evaluate(() => {
    const el = document.querySelector(".main") as HTMLElement | null;
    return el?.scrollTop ?? 0;
  });
}

async function setMainScrollTop(page: Page, y: number): Promise<void> {
  await page.evaluate((target) => {
    const el = document.querySelector(".main") as HTMLElement | null;
    if (el) el.scrollTop = target;
  }, y);
}

test.describe("F2.5 lightbox", () => {
  // -------------------------------------------------------------------------
  // Scenario 1: open from /library → walk → close → URL round-trip
  //
  // Verifies the snapshot/dispatcher round-trip: the grid click writes
  // a snapshot, navigates to /media/:id?from=library, arrows walk, and
  // Escape closes back to /library. Scroll-restore behavior is covered
  // separately by the paginated-album test below — the library seed is
  // shallow enough that .main barely scrolls and the assertion is noisy.
  // -------------------------------------------------------------------------
  test("library: open → walk → close round-trips URL", async ({ page }) => {
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();

    await page.locator("[data-media-id]").first().click();
    await expect(page).toHaveURL(/\/media\/.+\?from=library/);

    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/library/);
  });

  // -------------------------------------------------------------------------
  // Scenario 2: open from /sessions → walk → close
  // -------------------------------------------------------------------------
  test("sessions: open → walk → close", async ({ page }) => {
    await page.goto("/sessions");
    // Wait for the grid to hydrate — clicking before the first MediaCell
    // mounts would race the loadInitial response.
    await expect(page.locator("[data-media-id]").first()).toBeVisible();

    await page.locator("[data-media-id]").first().click();
    await expect(page).toHaveURL(/\/media\/.+\?from=sessions/);

    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/sessions/);
  });

  // -------------------------------------------------------------------------
  // Scenario 3: direct entry with from=library reconstructs the viewer
  //
  // The Lightbox component pages through MediaStore until the active id
  // appears, then derives navIds from the loaded months. lightbox-select-5
  // is seeded with 5 visible photos so prev/next must materialize.
  // -------------------------------------------------------------------------
  test("direct entry: /media/:id?from=library reconstructs", async ({ page }) => {
    await page.goto("/media/lightbox-select-5-id-002?from=library");
    await expect(page.locator(".lb-backdrop")).toBeVisible();
    // After reconstruction the prev/next buttons render — only when the
    // media row resolves AND the navIds list has at least one neighbour.
    await expect(page.locator(".lb-prev, .lb-next").first()).toBeVisible({
      timeout: 3_000,
    });
  });

  // -------------------------------------------------------------------------
  // Scenario 4: direct entry without `from` renders DirectMediaDetail
  //
  // MediaDetail.svelte is the dispatcher: from === undefined falls through
  // to DirectMediaDetail, which renders the Back to Library link and no
  // lightbox chrome.
  // -------------------------------------------------------------------------
  test("direct entry: /media/:id (no from) renders DirectMediaDetail", async ({
    page,
  }) => {
    await page.goto("/media/lightbox-select-5-id-002");
    await expect(
      page.getByRole("link", { name: /back to library/i }),
    ).toBeVisible();
    await expect(page.locator(".lb-backdrop")).toHaveCount(0);
  });

  // -------------------------------------------------------------------------
  // Scenario 5: keyboard bindings — info toggle + Esc close
  //
  // Lightbox's onKey handler maps `i` to infoOpen toggle and lets Escape
  // bubble to modalStack which calls close(). Editable-target guard means
  // typing into an input wouldn't trigger these — there are no inputs in
  // the v1 viewer so we drive directly off page-level key events.
  // -------------------------------------------------------------------------
  test("keyboard: i toggles info; Escape closes", async ({ page }) => {
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    await page.locator("[data-media-id]").first().click();
    await expect(page).toHaveURL(/\/media\/.+\?from=library/);

    // Open info — drawer (desktop) or sheet (mobile, max-width:768px).
    // Match either; the chromium project at default viewport is desktop.
    await page.keyboard.press("i");
    await expect(page.locator(".lb-drawer, .bs-sheet").first()).toBeVisible();

    // Toggle off.
    await page.keyboard.press("i");
    await expect(page.locator(".lb-drawer, .bs-sheet")).toHaveCount(0);

    // Esc closes the lightbox itself.
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/library/);
  });

  // -------------------------------------------------------------------------
  // Scenario 6: image source verification — preview + large requested
  //
  // The progressive loader requests size=preview then size=large for the
  // active photo. /original must NOT be requested (that's reserved for
  // Download). Throwing inside a route handler crashes the test runner;
  // instead, set a flag and assert after.
  // -------------------------------------------------------------------------
  test("image source: preview + large requested, no /original", async ({
    page,
  }) => {
    const requested: string[] = [];
    let originalSeen = false;

    await page.route("**/api/v1/media/*/thumb*", (route) => {
      requested.push(route.request().url());
      void route.continue();
    });
    await page.route("**/api/v1/media/*/original", (route) => {
      originalSeen = true;
      void route.continue();
    });

    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    await page.locator("[data-media-id]").first().click();
    await expect(page).toHaveURL(/\/media\/.+\?from=library/);

    // Allow the loader to run preview → large. A short wait is enough
    // because Image() preload settles synchronously when the response is
    // already in cache; otherwise it resolves on the network leg.
    await page.waitForTimeout(800);

    expect(requested.some((u) => u.includes("size=preview"))).toBe(true);
    expect(requested.some((u) => u.includes("size=large"))).toBe(true);
    expect(originalSeen).toBe(false);
  });

  // -------------------------------------------------------------------------
  // Scenario 7: paginated source (album) — scroll restore tolerance
  //
  // The album-30 fixture has 30 photos. Scrolling past the first row
  // and clicking a deep tile, the album's $effect captures scrollY into
  // lightboxSession. On Escape the album view remounts and ScrollRestore
  // converges on the captured Y. Tolerance is wider than library because
  // the album refetches members and may relay out.
  // -------------------------------------------------------------------------
  test("paginated source: scroll restore tolerates remount", async ({
    page,
  }) => {
    await page.goto("/albums/lightbox-album-30");
    await expect(page.locator("[data-media-id]").first()).toBeVisible();

    await setMainScrollTop(page, 2500);
    const startY = await getMainScrollTop(page);
    expect(startY).toBeGreaterThan(0);

    // Click a tile around the middle of the rendered list — the
    // captured returnFocusMediaId is whichever id is on the deep tile.
    const tiles = page.locator("[data-media-id]");
    const count = await tiles.count();
    await tiles.nth(Math.floor(count / 2)).click();
    await expect(page).toHaveURL(/\/media\/.+\?from=album/);

    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/albums\/lightbox-album-30/);

    // Album refetch + restore loop combined.
    await page.waitForTimeout(900);
    const endY = await getMainScrollTop(page);
    expect(Math.abs(endY - startY)).toBeLessThan(400);
  });
});
