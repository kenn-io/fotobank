import { test, expect, type Page } from "@playwright/test";

// ---------------------------------------------------------------------------
// W1 Search — covers the search surface: ⌘K opens the input, typing
// commits a query, results render, the indexing-status pill renders the
// completeness fraction, the under-80% banner appears when completeness
// is below 0.80 with a non-empty query, and the AI Inspection toggle
// reveals diagnostics badges.
//
// Seeded fixtures (cmd/e2e-server/main.go::seedSearchFixtures):
//   - 30 visible photos: search-fixture-vis-001 .. -030
//   - 5 hidden photos: search-fixture-hid-001 .. -005
//   - Active tag + caption per photo. Visible photos cycle keywords
//     (beach / mountain / sunset every 3rd); hidden photos use a
//     "hiddencache" marker so a search for that string verifies the
//     hidden gate.
//   - One active embedding generation with 22/30 mappings → completeness
//     ≈73% (under the 80% banner threshold).
//
// Deferred tests (per task plan): scrolling near bottom triggers next
// page (covered by VirtualGrid units); query_embedding_failed banner
// (the 503-every-Nth path is implemented in the mock, but driving it
// from a Playwright test without breaking the boot probe requires a
// per-test env-var toggle that v1 doesn't yet expose). See the inline
// TODOs in cmd/e2e-server for the failure-injection knobs.
// ---------------------------------------------------------------------------

// AI_PRE_ACKED mirrors ai.spec.ts: the suite default leaves the
// hidden-processing acknowledgement modal active, which means
// SettingsAI's "AI Inspection" toggle is gated behind the dialog.
// Tests that exercise the toggle must either set the env var or
// ack via the API up-front. We use the API path so this spec stays
// independent of how the harness was launched.
const AI_PRE_ACKED = process.env["FOTOBANK_E2E_AI_PRE_ACK"] === "1";

async function ackHiddenProcessing(page: Page): Promise<void> {
  const res = await page.request.post("/api/v1/ai/acknowledge", {
    headers: { "Content-Type": "application/json" },
    data: { kind: "hidden_processing" },
  });
  // 200 on first ack, 200 on subsequent (idempotent).
  expect([200, 204]).toContain(res.status());
}

test.describe("W1 Search", () => {
  // -------------------------------------------------------------------------
  // The AppHeader search input is reachable, focusable, and typing into
  // it commits a query through the SPA pipeline. The ⌘K / Ctrl+K
  // keyboard shortcut itself is verified in the AppHeader unit tests;
  // headless Chromium's modifier-key handling is brittle in
  // cross-platform CI (macOS/Linux divergence on Control+K), so this
  // spec focuses the input directly and validates the URL+result
  // pipeline that the shortcut would invoke.
  // -------------------------------------------------------------------------
  test("Search input commits query, navigates to /search?q=, and renders a result", async ({ page }) => {
    await page.goto("/library");
    // The header brand is a sync signal that the SPA shell has
    // mounted and the keydown listener is attached.
    await expect(page.getByText("fotobank", { exact: true })).toBeVisible();

    const searchInput = page.getByRole("searchbox", { name: "Search" });
    await searchInput.focus();
    await expect(searchInput).toBeFocused();

    // Type a query that matches one of the seeded keyword cycles.
    // 10 of the 30 visible photos carry a "beach" tag/caption. The
    // SearchBar contract is Enter-to-submit (no debounce-on-input
    // navigation), so press Enter explicitly to commit.
    await searchInput.fill("beach");
    await searchInput.press("Enter");

    // The URL contract is /search?q=beach (replace on /search, push otherwise).
    await expect(page).toHaveURL(/\/search\?.*q=beach/, { timeout: 5_000 });
    // At least one seeded result must render. MediaCell sets
    // aria-label="Photo <id>" for each grid cell. The first vis fixture
    // with the "beach" keyword is search-fixture-vis-001.
    await expect(
      page.getByLabel("Photo search-fixture-vis-001"),
    ).toBeVisible({ timeout: 5_000 });

    const thumbnail = page
      .getByLabel("Photo search-fixture-vis-001")
      .locator("img");
    await expect(thumbnail).toBeVisible();
    await expect
      .poll(() =>
        thumbnail.evaluate((image: HTMLImageElement) => image.naturalWidth),
      )
      .toBeGreaterThan(0);
  });

  // -------------------------------------------------------------------------
  // Adding a tag chip narrows the result set. The popover renders inline
  // (no click-to-open in v1), so we type into the tag autocomplete input
  // and click the matching suggestion. The chip strip then shows the
  // selected tag, and the URL gains a ?tag= param.
  // -------------------------------------------------------------------------
  test("Tag chip filter narrows results and surfaces in URL", async ({ page }) => {
    await page.goto("/search?q=photo");
    // Wait for the popover input to mount before typing — the route
    // hydrates from URL on mount, and an early focus race could drop
    // characters.
    const tagInput = page.getByTestId("search-filter-tag-input");
    await expect(tagInput).toBeVisible();

    await tagInput.fill("mou");
    // Suggestion list appears after the 200ms autocomplete debounce.
    const mountainSuggestion = page.getByTestId(
      "search-filter-tag-suggestion-mountain",
    );
    await expect(mountainSuggestion).toBeVisible({ timeout: 5_000 });
    await mountainSuggestion.click();

    // Chip strip shows the selected tag.
    await expect(page.getByTestId("chip-tag-mountain")).toBeVisible();
    // URL gains ?tag=mountain (alongside the ?q=photo we started with).
    await expect(page).toHaveURL(/tag=mountain/);
  });

  // -------------------------------------------------------------------------
  // The indexing-status pill renders when completeness < 1. The seed
  // pins 22 mappings against 32 eligible photos (30 visible + 2 AI
  // fixtures with thumb_status='ready'), landing completeness at
  // 22/32 = 0.6875 → 69% after rounding. The pill is visible
  // irrespective of whether a query has been typed (it surfaces
  // from the search response, which fires on hydration even with
  // empty q).
  // -------------------------------------------------------------------------
  test("Indexing-status pill shows the partial completeness", async ({ page }) => {
    await page.goto("/search?q=photo");
    const pill = page.getByTestId("indexing-status-pill");
    await expect(pill).toBeVisible({ timeout: 5_000 });
    // 22/32 = 0.6875 → Math.round(68.75) = 69. Match a forgiving 60-79
    // band so a future fixture-set tweak doesn't snap this assertion.
    await expect(pill).toContainText(/[67]\d%\s*indexed/);
  });

  // -------------------------------------------------------------------------
  // The under-80% banner appears when (a) the user has typed a query
  // AND (b) completeness < 0.80 AND (c) semantic_unavailable is false.
  // The seed lands at 73% with an active generation present, so the
  // banner should surface with a non-empty query.
  // -------------------------------------------------------------------------
  test("Under-80% banner shows when completeness < 0.80 with a query", async ({ page }) => {
    await page.goto("/search?q=photo");
    const banner = page.getByTestId("indexing-status-banner-under-eighty");
    await expect(banner).toBeVisible({ timeout: 5_000 });
    await expect(banner).toContainText(/Search is still indexing/);
  });

  // -------------------------------------------------------------------------
  // AI Inspection toggle reveals the per-cell diagnostics overlay. The
  // toggle persists via PUT /api/v1/settings/user/ai.inspection.
  //
  // Race avoidance: Search.svelte's inspectionStore.load() resolves
  // asynchronously; if the test types into the input before load
  // settles, the first search request fires with explain=false and
  // the engine omits score_components even though the URL re-hydrates
  // explain=true on a follow-up. The test sequences with a
  // hard-loaded /settings/ai page first so the Settings Provider
  // observes ai.inspection=true via the same store the search route
  // uses, then types into the search input — by which point the
  // inspectionStore is hydrated regardless of whether the search
  // route's own load() has resolved.
  // -------------------------------------------------------------------------
  test("AI Inspection on adds diagnostics badges to cells", async ({ page }) => {
    if (!AI_PRE_ACKED) {
      await ackHiddenProcessing(page);
    }
    // Flip ai.inspection=true via the user-settings API.
    const put = await page.request.put(
      "/api/v1/settings/user/ai.inspection",
      { data: { value: "true" } },
    );
    expect(put.status()).toBe(204);

    // Hit /settings/ai first so the SPA fetches ai.inspection and the
    // store is primed before the search route mounts. This avoids a
    // race where the search hydrates and issues a request before the
    // load() promise resolves and the explain-getter sees true.
    await page.goto("/settings/ai");
    await expect(page.getByText("fotobank", { exact: true })).toBeVisible();
    // Wait for the GET to complete by reading it back via API and
    // confirming the toggle landed; anchors the precondition before
    // we navigate to /search.
    const verify = await page.request.get(
      "/api/v1/settings/user/ai.inspection",
    );
    expect(verify.status()).toBe(200);
    expect(await verify.json()).toMatchObject({ value: "true" });

    // Navigate to /search?q=beach. Search.svelte constructs a fresh
    // inspectionStore on every mount and calls load() asynchronously,
    // so the first search request can race the load() promise and fly
    // without explain=true. The /search?explain=true wire request is
    // what we actually care about — wait for one to land before
    // asserting the badge.
    await page.goto("/search?q=beach");
    await expect(
      page.getByLabel("Photo search-fixture-vis-001"),
    ).toBeVisible({ timeout: 5_000 });

    // Click the SearchSortSegment "newest" button to force a fresh
    // search after the inspectionStore.load() has resolved. The
    // segmented control is rendered inside the search toolbar; the
    // store's setSort goes through issue() which reads
    // explainGetter() at issue time, picking up the now-true value.
    // The "newest" choice is also the de-facto sort for an empty-q
    // search so the result set is unchanged from a relevance baseline
    // (the engine still ranks by BM25/RRF candidates, then orders by
    // date — see internal/search/index/sqlitevec.go::fusedOrderBy).
    //
    // Race avoidance: register the waitForResponse promise BEFORE the
    // click so a fast same-machine response can't fire between the
    // click and the waiter. Promise.all sequences both registrations
    // synchronously — Playwright's waitForResponse is queued before
    // the click event is dispatched. This also drops the previous
    // fixed waitForTimeout(800) — relying on it can flake on slower
    // CI (response slips by) and on faster local runs (response
    // arrives before the waiter registers). The first /api/v1/search
    // request fired by the prior page.goto("/search?q=beach") has
    // already settled by the time we reach this point because the
    // result-cell visibility assertion above blocked on it.
    const newestBtn = page.getByRole("radio", { name: "Newest" });
    await Promise.all([
      page.waitForResponse(
        (resp) => {
          const url = new URL(resp.url());
          return (
            url.pathname === "/api/v1/search" &&
            url.searchParams.get("explain") === "true" &&
            resp.status() === 200
          );
        },
        { timeout: 8_000 },
      ),
      newestBtn.click(),
    ]);

    const badges = page.getByTestId("diagnostics-badge");
    await expect(badges.first()).toBeVisible({ timeout: 5_000 });
  });
});
