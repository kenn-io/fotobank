import { test, expect, type Page } from "@playwright/test";

// HIDDEN_UNCONFIGURED is true when the e2e server was started with
// FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1 — i.e. no passcode seeded. The
// configured-only scenarios below skip themselves under that flag so a
// dedicated unconfigured-server run only exercises the CTA path
// without faceplanting on configured-only assertions.
const HIDDEN_UNCONFIGURED = process.env["FOTOBANK_E2E_HIDDEN_UNCONFIGURED"] === "1";

// ---------------------------------------------------------------------------
// Shared helper: unlock the hidden vault with the seeded passcode.
// ---------------------------------------------------------------------------
async function unlock(page: Page) {
  await page.goto("/hidden");
  await page.getByPlaceholder("Passcode").fill("e2e-passcode");
  await page.getByRole("button", { name: "Unlock" }).click();
  // Wait for gate to unmount — the lock strip appears at the top when unlocked.
  await expect(
    page.locator("[role=status]", { hasText: "Hidden unlocked" }),
  ).toBeVisible();
}

// Configured-only scenarios depend on a seeded hidden passcode and
// must skip when the e2e server is started with
// FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1. The CTA-only tests at the
// bottom of the file require the inverse environment, so they live
// in their own describe block with their own beforeAll guard.
test.describe("F2.4 hidden privacy", () => {
  test.skip(
    HIDDEN_UNCONFIGURED,
    "configured-only scenarios; skipped under FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1",
  );
  // -------------------------------------------------------------------------
  // Scenario 1: Sidebar Hidden entry visible in BROWSE
  // -------------------------------------------------------------------------
  test("sidebar Hidden link is present and navigates to /hidden", async ({ page }) => {
    await page.goto("/library");
    // The AppHeader nav also exposes a "Hidden" link — scope to the
    // sidebar (`<aside role="complementary">`) so this stays a sidebar
    // contract test rather than tripping strict mode against two
    // matches. exact: true prevents substring matches against
    // "Photo hidden-target-1" grid cells.
    const hiddenLink = page
      .getByRole("complementary")
      .getByRole("link", { name: "Hidden", exact: true });
    await expect(hiddenLink).toBeVisible();
    await hiddenLink.click();
    await expect(page).toHaveURL(/\/hidden$/);
    // The gate renders — passcode form or CTA — confirming the route works.
    await expect(page.getByText("fotobank")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Scenario 2: /hidden shows CTA when not configured
  //
  // The full CTA visual test requires a server started with
  // FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1 (skipped below). Instead, we verify
  // the positive case: API state reflects configured=true for the default run.
  // -------------------------------------------------------------------------
  test("API state reflects configured=true when credential is seeded", async ({ page }) => {
    const res = await page.request.get("/api/v1/auth/hidden/state");
    expect(res.status()).toBe(200);
    const body = (await res.json()) as { configured: boolean };
    expect(body.configured).toBe(true);
  });

  // -------------------------------------------------------------------------
  // Scenario 3: Wrong passcode shows error; no cookie set
  // -------------------------------------------------------------------------
  test("wrong passcode shows error and keeps gate visible", async ({ page }) => {
    await page.goto("/hidden");
    await page.getByPlaceholder("Passcode").fill("wrong-passcode");
    await page.getByRole("button", { name: "Unlock" }).click();
    // Inline error copy — HiddenGate renders "Passcode incorrect."
    await expect(page.getByText("Passcode incorrect.")).toBeVisible();
    // Gate still rendered — passcode field stays in DOM.
    await expect(page.getByPlaceholder("Passcode")).toBeVisible();
    // Lock strip must NOT be present — no cookie was issued.
    await expect(
      page.locator("[role=status]", { hasText: "Hidden unlocked" }),
    ).toHaveCount(0);
  });

  // -------------------------------------------------------------------------
  // Scenario 4 + 5: Correct passcode unlocks; top-bar strip appears
  // -------------------------------------------------------------------------
  test("correct passcode unlocks and shows lock strip with countdown", async ({ page }) => {
    await page.goto("/hidden");
    await page.getByPlaceholder("Passcode").fill("e2e-passcode");
    await page.getByRole("button", { name: "Unlock" }).click();

    // Scenario 4: gate unmounts — lock strip appears.
    const strip = page.locator("[role=status]", { hasText: "Hidden unlocked" });
    await expect(strip).toBeVisible();

    // Scenario 5: strip has Lock button and countdown.
    await expect(strip.getByRole("button", { name: "Lock now" })).toBeVisible();
    const countdown = strip.locator("[aria-label='Time remaining']");
    await expect(countdown).toBeVisible();
    await expect(countdown).toHaveText(/^\d+:\d{2}$/);

    // The seeded hidden-prehidden-1 should appear in the hidden grid.
    await expect(page.getByLabel("Photo hidden-prehidden-1")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Scenario 6: "Lock now" button locks immediately
  // -------------------------------------------------------------------------
  test("Lock now button hides strip and re-shows gate on /hidden", async ({ page }) => {
    await unlock(page);

    await page
      .locator("[role=status]", { hasText: "Hidden unlocked" })
      .getByRole("button", { name: "Lock now" })
      .click();

    // Strip must disappear.
    await expect(
      page.locator("[role=status]", { hasText: "Hidden unlocked" }),
    ).toHaveCount(0);

    // Navigate to /hidden — gate re-appears.
    await page.goto("/hidden");
    await expect(page.getByPlaceholder("Passcode")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Scenario 7: Tab-hidden auto-lock
  //
  // Playwright cannot trigger real visibilitychange semantics from outside
  // the browser. We use page.evaluate to override visibilityState and
  // dispatch the event, which App.svelte's handler picks up and calls
  // hiddenStore.lock({keepalive:true}) — an optimistic synchronous update.
  // -------------------------------------------------------------------------
  test("visibilitychange auto-locks the session", async ({ page }) => {
    await unlock(page);

    await expect(
      page.locator("[role=status]", { hasText: "Hidden unlocked" }),
    ).toBeVisible();

    await page.evaluate(() => {
      Object.defineProperty(document, "visibilityState", {
        value: "hidden",
        configurable: true,
      });
      document.dispatchEvent(new Event("visibilitychange"));
    });

    // Strip disappears after optimistic lock.
    await expect(
      page.locator("[role=status]", { hasText: "Hidden unlocked" }),
    ).toHaveCount(0);
  });

  // -------------------------------------------------------------------------
  // Scenario 8: Hide from Library — media disappears from grid
  // Uses hidden-target-1 (seeded visible).
  // -------------------------------------------------------------------------
  test("hide from Library removes media from grid; visible in /hidden after unlock", async ({
    page,
  }) => {
    page.on("dialog", (d) => d.accept());

    await page.goto("/library");
    await expect(page.getByLabel("Photo hidden-target-1")).toBeVisible();

    await page.getByLabel("Photo hidden-target-1").click({ modifiers: ["Meta"] });
    await expect(page.getByText("1 selected")).toBeVisible();
    await page.getByRole("button", { name: "Hide" }).click();

    // Disappears from Library.
    await expect(page.getByLabel("Photo hidden-target-1")).toHaveCount(0);

    // After unlock, visible in /hidden.
    await unlock(page);
    await expect(page.getByLabel("Photo hidden-target-1")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Scenario 9: Unhide from /hidden — moves back to Library
  // Uses hidden-prehidden-1 (seeded hidden).
  // -------------------------------------------------------------------------
  test("unhide from /hidden makes media visible in Library again", async ({ page }) => {
    page.on("dialog", (d) => d.accept());

    await unlock(page);

    // hidden-prehidden-1 is seeded with hidden_at set.
    await expect(page.getByLabel("Photo hidden-prehidden-1")).toBeVisible();

    await page.getByLabel("Photo hidden-prehidden-1").click({ modifiers: ["Meta"] });
    await expect(page.getByText("1 selected")).toBeVisible();
    await page.getByRole("button", { name: "Unhide" }).click();

    // Disappears from /hidden.
    await expect(page.getByLabel("Photo hidden-prehidden-1")).toHaveCount(0);

    // Visible in /library.
    await page.goto("/library");
    await expect(page.getByLabel("Photo hidden-prehidden-1")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Scenario 10: Library list NOT affected by unlock cookie
  //
  // The library endpoint must exclude hidden rows even when the unlock
  // cookie is present. This test establishes its own hidden state via
  // the API (rather than depending on scenario 8 having run first), so
  // running this spec alone, retrying it, or sharding the file does not
  // produce false failures.
  // -------------------------------------------------------------------------
  test("library API excludes hidden rows even when unlock cookie is present", async ({
    page,
  }) => {
    // Establish hidden state ourselves via the registered bulk endpoint
    // (POST /api/v1/media/hidden:bulk) so this test works regardless of
    // whether scenario 8 has run earlier. The bulk endpoint is
    // idempotent — re-hiding an already-hidden id is a successful no-op
    // in the response's `succeeded` list.
    await unlock(page);
    const hideRes = await page.request.post("/api/v1/media/hidden:bulk", {
      headers: { "Content-Type": "application/json" },
      data: { media_ids: ["hidden-target-1"] },
    });
    expect(hideRes.status()).toBe(200);

    // Hit /api/v1/media — must exclude hidden-target-1 regardless of unlock.
    const res = await page.request.get("/api/v1/media?limit=200&offset=0");
    expect(res.status()).toBe(200);
    const body = (await res.json()) as { items: Array<{ id: string }> };
    const ids = body.items.map((it) => it.id);
    expect(ids).not.toContain("hidden-target-1");
  });

  // -------------------------------------------------------------------------
  // Scenario 11: Sidecar cascade
  //
  // Uses hidden-cascade-primary / hidden-cascade-sidecar (seeded separately
  // so hiding them doesn't contaminate pair-fixture-* fixtures used by
  // library.spec.ts and albums.spec.ts).
  // -------------------------------------------------------------------------
  test("hiding a primary hides its sidecar (cascade)", async ({ page }) => {
    page.on("dialog", (d) => d.accept());

    // Verify sidecar is currently accessible (visible = no hidden_at).
    const sidecarBefore = await page.request.get(
      "/api/v1/media/hidden-cascade-sidecar",
    );
    expect(sidecarBefore.status()).toBe(200);

    // Hide the primary via the Library UI.
    await page.goto("/library");
    await expect(page.getByLabel("Photo hidden-cascade-primary")).toBeVisible();
    await page
      .getByLabel("Photo hidden-cascade-primary")
      .click({ modifiers: ["Meta"] });
    await page.getByRole("button", { name: "Hide" }).click();
    await expect(page.getByLabel("Photo hidden-cascade-primary")).toHaveCount(0);

    // Sidecar must now be inaccessible: hidden_at cascaded from primary.
    // GET /media/:id on a hidden sidecar without unlock cookie → 404 (anti-enum).
    const sidecarAfter = await page.request.get(
      "/api/v1/media/hidden-cascade-sidecar",
    );
    expect(sidecarAfter.status()).toBe(404);
  });

  // -------------------------------------------------------------------------
  // Scenario 12: Album hidden_count chip
  //
  // Uses hidden-album-target-1 (seeded visible, member of E2E Italy 2025)
  // so hiding it doesn't contaminate gps-fixture-1 (used by shares tests).
  // -------------------------------------------------------------------------
  test("album header shows hidden_count chip after hiding a member", async ({ page }) => {
    page.on("dialog", (d) => d.accept());

    await page.goto("/albums");
    await expect(page.getByText("E2E Italy 2025")).toBeVisible();
    await page.getByText("E2E Italy 2025").click();
    await expect(page).toHaveURL(/\/albums\/[a-f0-9-]+$/);

    // No hidden chip before hiding anything.
    await expect(page.locator(".hidden-chip")).toHaveCount(0);

    // Wait for the album grid to load (detail fetch may race the navigate).
    await expect(page.getByLabel("Photo hidden-album-target-1")).toBeVisible();

    // Select and hide hidden-album-target-1.
    await page
      .getByLabel("Photo hidden-album-target-1")
      .click({ modifiers: ["Meta"] });
    await expect(page.getByText("1 selected")).toBeVisible();
    await page.getByRole("button", { name: "Hide" }).click();

    // Photo disappears from album grid.
    await expect(page.getByLabel("Photo hidden-album-target-1")).toHaveCount(0);

    // After AlbumDetailStore.refreshMeta(), the header must show the chip.
    await expect(page.locator(".hidden-chip")).toBeVisible();
    await expect(page.locator(".hidden-chip")).toContainText("hidden");
  });

  // -------------------------------------------------------------------------
  // Scenario 13: Lockout — 5+ wrong passcodes triggers 429
  //
  // The lockout fires after failureThreshold (5) attempts within the window.
  // With FOTOBANK_E2E_LOCKOUT_WINDOW set to a short duration the lockout
  // resolves quickly; in the default production run (60s window) the test
  // just verifies the UI copy appears after enough failures.
  // -------------------------------------------------------------------------
  test("5 wrong passcodes trigger lockout with countdown copy", async ({ page }) => {
    await page.goto("/hidden");

    const passcodeField = page.getByPlaceholder("Passcode");
    const unlockBtn = page.getByRole("button", { name: "Unlock" });

    // Submit 5 wrong passcodes. Each increments the failure counter.
    // Fill the field first (button is disabled when empty), then wait for
    // the button to be enabled (not submitting from a prior attempt) before
    // clicking. After each click, wait for the error copy before proceeding.
    for (let i = 0; i < 5; i++) {
      await passcodeField.fill(`wrong-lockout-${i}`);
      await expect(unlockBtn).toBeEnabled();
      await unlockBtn.click();
      // Wait for the error copy before the next attempt to avoid races.
      await expect(
        page.getByText(/Passcode incorrect\.|Too many attempts/),
      ).toBeVisible();
    }

    // Submit one more — should now hit the lockout.
    await passcodeField.fill("wrong-lockout-5");
    await expect(unlockBtn).toBeEnabled();
    await unlockBtn.click();

    // HiddenGate renders: "Too many attempts. Try again in N min."
    await expect(page.getByText(/Too many attempts/)).toBeVisible({ timeout: 10_000 });
  });

  // -------------------------------------------------------------------------
  // Scenario 14: Grantee never sees hidden
  //
  // A dedicated share "Hidden grantee e2e share" includes hidden-share-member-1.
  // We verify server-side enforcement by checking the preview media array
  // shrinks when hidden-share-member-1 is hidden — GetByUUID filters
  // hidden_at IS NULL for media_set scopes, so ExpandScope.MediaIDs
  // already excludes hidden items and PreviewScope.Media reflects that.
  //
  // Uses a dedicated fixture/share to avoid contaminating gps-fixture-1
  // and "Active e2e share" which are relied on by shares.spec.ts.
  // -------------------------------------------------------------------------
  test("share preview media count drops when shared media is hidden", async ({ page }) => {
    page.on("dialog", (d) => d.accept());

    // Find the "Hidden grantee e2e share" UUID.
    const listRes = await page.request.get("/api/v1/shares?limit=50&offset=0");
    expect(listRes.status()).toBe(200);
    const listBody = (await listRes.json()) as {
      items: Array<{ uuid: string; label: string }>;
    };
    const hiddenShare = listBody.items.find(
      (s) => s.label === "Hidden grantee e2e share",
    );
    expect(hiddenShare).toBeDefined();
    const uuid = hiddenShare!.uuid;

    // Get preview before hiding — media array must be non-empty.
    const previewBefore = await page.request.get(`/api/v1/shares/${uuid}/preview`);
    expect(previewBefore.status()).toBe(200);
    const countBefore = (
      (await previewBefore.json()) as { media: unknown[] }
    ).media.length;
    expect(countBefore).toBeGreaterThan(0);

    // Hide hidden-share-member-1 via the Library UI.
    await page.goto("/library");
    await expect(page.getByLabel("Photo hidden-share-member-1")).toBeVisible();
    await page.getByLabel("Photo hidden-share-member-1").click({ modifiers: ["Meta"] });
    await page.getByRole("button", { name: "Hide" }).click();
    await expect(page.getByLabel("Photo hidden-share-member-1")).toHaveCount(0);

    // Preview must now show a smaller (or zero) media array.
    const previewAfter = await page.request.get(`/api/v1/shares/${uuid}/preview`);
    expect(previewAfter.status()).toBe(200);
    const countAfter = (
      (await previewAfter.json()) as { media: unknown[] }
    ).media.length;
    expect(countAfter).toBeLessThan(countBefore);
  });

  // -------------------------------------------------------------------------
  // Scenario 15: Hide button gated by hiddenConfigured
  //
  // In the default run (credential seeded), selecting a photo in Library
  // shows the Hide button. The unconfigured variant is marked skip.
  // -------------------------------------------------------------------------
  test("Hide button appears in Library when hiddenConfigured=true", async ({ page }) => {
    await page.goto("/library");
    // Wait for at least one non-hidden photo to be visible.
    // gps-fixture-1 may have been hidden by scenario 14, so use a set of
    // candidates and pick the first that's visible.
    const candidates = [
      "no-gps-fixture-1",
      "pair-fixture-primary",
      "hidden-prehidden-1",  // may be visible again after scenario 9
    ];
    let selected = false;
    for (const id of candidates) {
      const cell = page.getByLabel(`Photo ${id}`);
      if ((await cell.count()) > 0) {
        await cell.click({ modifiers: ["Meta"] });
        selected = true;
        break;
      }
    }
    expect(selected, "at least one candidate photo visible in library").toBe(true);
    await expect(page.getByText(/\d+ selected/)).toBeVisible();

    // Hide button must be present (hiddenConfigured=true from seeded credential).
    await expect(page.getByRole("button", { name: "Hide" })).toBeVisible();
  });

});

// Unconfigured-only scenarios. The describe-level skip is the inverse
// of the configured block above: this only runs when the e2e server
// was started with FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1, which means no
// passcode is seeded and the UI must hide the Hide affordance.
test.describe("F2.4 hidden privacy (unconfigured server)", () => {
  test.skip(
    !HIDDEN_UNCONFIGURED,
    "requires FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1 server",
  );

  test("Hide button absent when hiddenConfigured=false", async ({ page }) => {
    await page.goto("/library");
    const firstPhoto = page.getByLabel(/^Photo /).first();
    await expect(firstPhoto).toBeVisible();
    await firstPhoto.click({ modifiers: ["Meta"] });
    await expect(page.getByText("1 selected")).toBeVisible();
    // No Hide button when credential is not seeded.
    await expect(page.getByRole("button", { name: "Hide" })).toHaveCount(0);
  });

  // /hidden CTA: when no credential is seeded the gate must render
  // setup copy instead of the passcode form. Mirrors the
  // configured-side scenario 2 (which asserts API state.configured=true
  // for the seeded run); this asserts state.configured=false here AND
  // verifies the visible UI shows the unconfigured CTA. Restored after
  // the configured/unconfigured describe split — without it, regressions
  // in the setup CTA would not be caught by either run.
  test("CTA visible and API state reflects configured=false", async ({ page }) => {
    const res = await page.request.get("/api/v1/auth/hidden/state");
    expect(res.status()).toBe(200);
    const body = (await res.json()) as { configured: boolean };
    expect(body.configured).toBe(false);

    await page.goto("/hidden");
    // Passcode form must NOT be visible — credential is unconfigured.
    await expect(page.getByPlaceholder("Passcode")).toHaveCount(0);
    // Setup CTA copy is rendered by HiddenGate when configured=false.
    // HiddenGate.svelte renders "Hidden privacy isn't set up." with a
    // recipe to run `fotobank hidden setup`. Match that copy so a
    // regression in the gate's CTA is caught by this run.
    await expect(page.getByText(/Hidden privacy isn't set up\./)).toBeVisible();
    await expect(page.getByText(/fotobank hidden setup/)).toBeVisible();
  });
});
