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

  // -------------------------------------------------------------------------
  // Scenario 8: selection-walk uses the selected ids when N>1 selected
  //
  // Library.openMedia narrows navIds to the selected set when the click
  // target is part of a multi-selection. ArrowRight should step through
  // the selected ids in document order; the last one is the boundary.
  // -------------------------------------------------------------------------
  test("selection: walks selected ids when N>1 selected and opened id is in selection", async ({
    page,
  }) => {
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    const ids = [
      "lightbox-select-5-id-001",
      "lightbox-select-5-id-002",
      "lightbox-select-5-id-003",
      "lightbox-select-5-id-004",
      "lightbox-select-5-id-005",
    ] as const;
    for (const id of ids) {
      await page
        .locator(`[data-media-id="${id}"]`)
        .click({ modifiers: ["Meta"] });
    }
    // Wait for the action bar to confirm the selection settled before
    // the plain click; otherwise the Meta-click race can leave the last
    // id un-selected when openMedia reads selection.ids.
    await expect(page.getByText("5 selected")).toBeVisible();

    // Plain-click the 3rd selected id to open with selection-walk.
    await page.locator(`[data-media-id="${ids[2]}"]`).click();
    await expect(page).toHaveURL(
      new RegExp(`/media/${ids[2]}\\?from=library`),
    );
    await page.keyboard.press("ArrowRight");
    await expect(page).toHaveURL(
      new RegExp(`/media/${ids[3]}\\?from=library`),
    );
    await page.keyboard.press("ArrowRight");
    await expect(page).toHaveURL(
      new RegExp(`/media/${ids[4]}\\?from=library`),
    );
    // Boundary: one more ArrowRight stays on the last selected id.
    await page.keyboard.press("ArrowRight");
    await expect(page).toHaveURL(
      new RegExp(`/media/${ids[4]}\\?from=library`),
    );
  });

  // -------------------------------------------------------------------------
  // Scenario 9: plain-click an unselected tile uses the FULL source list
  //
  // When the click target is NOT in the current selection, openMedia
  // ignores the selection and walks the full flattened library. The
  // select-5 fixture is sorted DESC by import time (id-001 newest,
  // id-005 oldest); plain-clicking id-004 (unselected) and pressing
  // ArrowRight should land on id-005 — the next library tile in DESC
  // order. The selection walk would have stopped at id-003 (last in
  // [id-001,id-002,id-003]), so reaching id-005 proves the full-library
  // walk is in effect.
  // -------------------------------------------------------------------------
  test("selection: plain-click an unselected tile uses full source list", async ({
    page,
  }) => {
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    const selectedIds = [
      "lightbox-select-5-id-001",
      "lightbox-select-5-id-002",
      "lightbox-select-5-id-003",
    ];
    for (const id of selectedIds) {
      await page
        .locator(`[data-media-id="${id}"]`)
        .click({ modifiers: ["Meta"] });
    }
    await expect(page.getByText("3 selected")).toBeVisible();

    // Plain-click a tile outside the selection.
    await page.locator(`[data-media-id="lightbox-select-5-id-004"]`).click();
    await expect(page).toHaveURL(
      /\/media\/lightbox-select-5-id-004\?from=library/,
    );
    // Full library walk — ArrowRight advances to id-005, which is NOT
    // in the selection. A selection-walk would have nothing to advance
    // to from id-004 (because id-004 isn't in the selected set).
    await page.keyboard.press("ArrowRight");
    await expect(page).toHaveURL(
      /\/media\/lightbox-select-5-id-005\?from=library/,
    );
  });

  // -------------------------------------------------------------------------
  // Scenario 10: album hide → advance to next visible
  //
  // After Hide succeeds, LightboxActions calls lightboxSession.removeIds
  // and onActionDone is supposed to advance to nav.nextId (or close if
  // navIds is exhausted). The plan spec at lines 2745-2750 reads:
  //   "If active id was removed, advance to nav.nextId or nav.prevId;
  //    else stay."
  //
  // CURRENT BUG: Lightbox.onActionDone reads `nav.nextId` AFTER
  // lightboxSession.removeIds prunes the active id. Because `nav` is a
  // $derived(computeNav(navIds, id)) and `id` is no longer in navIds,
  // computeNav returns -1 with both prev/next null. advanceTo would
  // resolve to null and close() would run unconditionally.
  //
  // Fixed by reordering LightboxActions to call onDone BEFORE pruning
  // navIds; onActionDone captures advanceTo while nav still reflects
  // pre-mutation state, then performs the navigation.
  // -------------------------------------------------------------------------
  test("album: hide active item advances to next visible", async ({
    page,
  }) => {
    await page.goto("/albums/lightbox-album-30");
    await expect(
      page.locator(`[data-media-id="lightbox-album-30-id-001"]`),
    ).toBeVisible();

    // Stub window.confirm before the Hide click — LightboxActions.onHide
    // bails out unconditionally if confirm() returns false.
    await page.evaluate(() => {
      window.confirm = () => true;
    });

    await page.locator(`[data-media-id="lightbox-album-30-id-001"]`).click();
    await expect(page).toHaveURL(
      /\/media\/lightbox-album-30-id-001\?from=album:/,
    );

    await page.getByRole("button", { name: /^Hide$/i }).click();
    // After hide: lightbox should advance to the next album member.
    // navTo() runs the `from` value through encodeURIComponent, so the
    // colon between "album" and the album id arrives as %3A. Match
    // either form so the test doesn't drift if the encoding changes.
    await expect(page).not.toHaveURL(/\/media\/lightbox-album-30-id-001/);
    await expect(page).toHaveURL(
      /\/media\/.+\?from=album(?::|%3A)lightbox-album-30/,
    );
  });

  // -------------------------------------------------------------------------
  // Scenario 11: hidden grid → unhide → close → row re-appears in library
  //
  // Mirrors hidden.spec.ts scenario 9 but enters via the lightbox: open
  // a hidden cell, click Unhide (stubbed confirm), Esc out, then verify
  // /library lists the formerly-hidden id. lightboxSession.removeIds
  // prunes navIds and the unhide effect calls mediaStore.mergeRaw to
  // re-introduce the row to the visible store with hidden_at = null.
  // -------------------------------------------------------------------------
  test("hidden: walk → unhide → close → row visible in library", async ({
    page,
  }) => {
    await page.goto("/hidden");
    await page.getByPlaceholder("Passcode").fill("e2e-passcode");
    await page.getByRole("button", { name: "Unlock" }).click();
    await expect(
      page.locator("[role=status]", { hasText: "Hidden unlocked" }),
    ).toBeVisible();

    await expect(
      page.locator(`[data-media-id="lightbox-hidden-2-id-001"]`),
    ).toBeVisible();
    await page.evaluate(() => {
      window.confirm = () => true;
    });
    await page.locator(`[data-media-id="lightbox-hidden-2-id-001"]`).click();
    await expect(page).toHaveURL(
      /\/media\/lightbox-hidden-2-id-001\?from=hidden/,
    );

    await page.getByRole("button", { name: /^Unhide$/i }).click();
    // After unhide the lightbox advances or closes; either way Esc
    // returns to /hidden.
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/hidden$/);

    // /library now shows the unhidden id.
    await page.goto("/library");
    await expect(
      page.locator(`[data-media-id="lightbox-hidden-2-id-001"]`),
    ).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Scenario 12: direct entry to /media/:id?from=hidden while locked
  //
  // The reconstruction effect issues GET /api/v1/hidden/media; the
  // server returns 403 when the unlock cookie is missing. The Lightbox
  // catches the 403 and router.navigate("/hidden", {replace: true}).
  // -------------------------------------------------------------------------
  test("hidden direct entry while locked redirects to /hidden", async ({
    page,
  }) => {
    // Start from the unlocked state so the cookie exists, then call the
    // lock endpoint to clear it. page.request shares the browser
    // context's cookie jar, so the subsequent page.goto runs locked.
    await page.goto("/hidden");
    await page.getByPlaceholder("Passcode").fill("e2e-passcode");
    await page.getByRole("button", { name: "Unlock" }).click();
    await expect(
      page.locator("[role=status]", { hasText: "Hidden unlocked" }),
    ).toBeVisible();
    await page.request.post("/api/v1/auth/hidden/lock");

    await page.goto("/media/lightbox-hidden-2-id-001?from=hidden");
    await expect(page).toHaveURL(/\/hidden$/);
  });

  // -------------------------------------------------------------------------
  // Scenario 13: hidden cross-context — from=library on a hidden row
  //
  // Lightbox's hiddenCrossContext flag flips on when the row resolves
  // hidden but `from` is not "hidden". fallbackMode is true so the
  // viewer renders the fallback shell wrapping DirectMediaDetail; the
  // prev/next nav buttons are not in the DOM in fallback mode.
  //
  // Uses id-002 (still hidden) instead of id-001 — Scenario 11 unhides
  // id-001, and the e2e suite runs sequentially with workers=1, so
  // id-001 is no longer hidden by the time this test runs.
  // -------------------------------------------------------------------------
  test("hidden cross-context: from=library + hidden_at != null → fallback shell", async ({
    page,
  }) => {
    // Unlock so /api/v1/media/:id returns the hidden row body.
    await page.goto("/hidden");
    await page.getByPlaceholder("Passcode").fill("e2e-passcode");
    await page.getByRole("button", { name: "Unlock" }).click();
    await expect(
      page.locator("[role=status]", { hasText: "Hidden unlocked" }),
    ).toBeVisible();

    await page.goto("/media/lightbox-hidden-2-id-002?from=library");
    await expect(page.locator(".lb-backdrop")).toBeVisible();
    await expect(page.locator(".lb-prev")).toHaveCount(0);
    await expect(page.locator(".lb-next")).toHaveCount(0);
  });

  // -------------------------------------------------------------------------
  // Scenario 14: stacked modals — Esc closes Add-to-album first, then lightbox
  //
  // modalStack guarantees Esc only closes the topmost modal. The Add
  // modal pushes onto the stack on open; Esc invokes its onEscape and
  // pops. A second Esc reaches the lightbox's onEscape and closes back
  // to /library.
  // -------------------------------------------------------------------------
  test("stacked modals: Esc closes Add-to-album first, then lightbox", async ({
    page,
  }) => {
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    await page.locator("[data-media-id]").first().click();
    await expect(page).toHaveURL(/\/media\/.+\?from=library/);

    await page.getByRole("button", { name: /add to album/i }).click();
    await expect(page.getByRole("dialog")).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    // Lightbox still open.
    await expect(page.locator(".lb-backdrop")).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/library/);
  });

  // -------------------------------------------------------------------------
  // Scenario 15: stacked modals — arrow keys do not advance the lightbox
  //
  // Lightbox.onKey gates on modalStack.isTopmost(modalId) so an open
  // Add-to-album modal swallows ArrowRight. The URL must not change
  // while the modal is up.
  // -------------------------------------------------------------------------
  test("stacked modals: arrows do not advance lightbox while Add-to-album is open", async ({
    page,
  }) => {
    await page.goto("/library");
    await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
    await page.locator("[data-media-id]").first().click();
    await expect(page).toHaveURL(/\/media\/.+\?from=library/);
    const startUrl = page.url();
    await page.getByRole("button", { name: /add to album/i }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.keyboard.press("ArrowRight");
    expect(page.url()).toBe(startUrl);
  });

  // -------------------------------------------------------------------------
  // Scenario 16: fallback delegation — bogus album id
  //
  // Reconstruction issues GET /api/v1/albums/bogus/media; server returns
  // an empty page (or 404) and the Lightbox flips reconstructionState
  // to "failed". fallbackMode renders the chrome + DirectMediaDetail;
  // no prev/next buttons appear.
  // -------------------------------------------------------------------------
  test("fallback: /media/:id?from=album:bogus renders fallback shell + DirectMediaDetail", async ({
    page,
  }) => {
    await page.goto("/media/lightbox-select-5-id-001?from=album:bogus");
    await expect(page.locator(".lb-backdrop")).toBeVisible();
    await expect(page.locator(".lb-prev")).toHaveCount(0);
    await expect(page.locator(".lb-next")).toHaveCount(0);
    // Esc closes; router.back falls back to /library when history is
    // empty — assertion is just that the navigation completes without
    // error. We don't assert the exact target href since the test
    // launches direct-entry with no history stack.
    await page.keyboard.press("Escape");
  });
});
