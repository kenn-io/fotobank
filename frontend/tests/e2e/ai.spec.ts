import { test, expect, type Page } from "@playwright/test";

// ---------------------------------------------------------------------------
// F4 AI — covers the acknowledgement gate, the lightbox tag/caption/provenance
// rendering, and the per-photo retry path. Default e2e-server runs with the
// hidden-processing acknowledgement NOT pre-recorded so the ack modal is
// testable; tests that need a post-ack state call POST /api/v1/ai/acknowledge
// directly via page.request to flip the gate.
//
// Seeded fixtures (cmd/e2e-server/main.go::seedAIFixtures):
//   - ai-fixture-tagged-1: tags + caption present
//   - ai-fixture-failed-1: tags present, caption_failure recorded
// ---------------------------------------------------------------------------

const AI_PRE_ACKED = process.env["FOTOBANK_E2E_AI_PRE_ACK"] === "1";

async function ackHiddenProcessing(page: Page): Promise<void> {
  const res = await page.request.post("/api/v1/ai/acknowledge", {
    headers: { "Content-Type": "application/json" },
    data: { kind: "hidden_processing" },
  });
  expect(res.status()).toBe(200);
}

test.describe("F4 AI", () => {
  // -------------------------------------------------------------------------
  // Acknowledgement gate — only meaningful when the server starts unacked.
  // FOTOBANK_E2E_AI_PRE_ACK=1 skips this branch because the ack row is
  // already present and SettingsAI never renders the dialog.
  // -------------------------------------------------------------------------
  test("acknowledgement gate parks workers; ack reveals task cards", async ({
    page,
  }) => {
    test.skip(
      AI_PRE_ACKED,
      "ack modal only renders when the server is started unacked",
    );

    await page.goto("/settings/ai");
    // Match the ack dialog copy with a regex so a small wording tweak in
    // SettingsAI.svelte doesn't drift this assertion.
    await expect(
      page.getByText(/Before AI starts processing/i),
    ).toBeVisible();
    // Task cards are gated behind paused_reason !== "acknowledgement_required".
    await expect(page.getByRole("article")).toHaveCount(0);

    await page
      .getByRole("button", { name: /Acknowledge and start workers/i })
      .click();

    // After ack the modal unmounts and the task cards render.
    await expect(
      page.getByText(/Before AI starts processing/i),
    ).toHaveCount(0);
    // Both task cards (tag + caption) appear once paused_reason clears.
    // SettingsAI renders a <strong>Tag</strong> / <strong>Caption</strong>
    // header inside each .task-card; a getByRole locator would also match
    // unrelated <strong> nodes elsewhere in the page, so we scope to the
    // task-card article.
    await expect(page.locator(".task-card")).toHaveCount(2);
  });

  // -------------------------------------------------------------------------
  // Status dot — once the user acknowledges, the global AIStatusDot must
  // appear. We ack via the API to keep this test independent of the dialog.
  // -------------------------------------------------------------------------
  test("AIStatusDot is visible in shell after ack", async ({ page }) => {
    if (!AI_PRE_ACKED) {
      await ackHiddenProcessing(page);
    }
    await page.goto("/library");
    // The dot is an <a class="ai-dot"> when state !== "hidden". Wait for
    // App.svelte's mount-effect to fetch /ai/health and update the store.
    await expect(page.locator("a.ai-dot")).toBeVisible();
  });

  // -------------------------------------------------------------------------
  // Lightbox: seeded ai-fixture-tagged-1 has two active tags + active
  // caption. LightboxAI is mounted inside LightboxMetadata, which only
  // renders when the info drawer is open (toggle via "i"). We enter via
  // ?from=library so the full lightbox shell mounts (DirectMediaDetail
  // does not embed LightboxAI). Verifies chips, caption text, and the
  // provenance link pointing at /settings/ai.
  // -------------------------------------------------------------------------
  test("lightbox shows tags + caption + provenance link", async ({ page }) => {
    if (!AI_PRE_ACKED) {
      await ackHiddenProcessing(page);
    }
    await page.goto("/media/ai-fixture-tagged-1?from=library");
    // Wait for the lightbox shell to mount before toggling the info
    // panel; pressing "i" before the keyboard handler attaches drops
    // the keystroke.
    await expect(page.locator(".lb-backdrop")).toBeVisible();
    await page.keyboard.press("i");
    await expect(page.locator(".lb-drawer, .bs-sheet").first()).toBeVisible();

    await expect(page.getByText("e2e-tag-a")).toBeVisible();
    await expect(page.getByText("e2e-tag-b")).toBeVisible();
    await expect(
      page.getByText(/An e2e test photo of a small dog on a beach\./),
    ).toBeVisible();
    // Provenance link text is the model id; href is /settings/ai.
    const link = page.getByRole("link", { name: /qwen2\.5-vl/ });
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute("href", "/settings/ai");
  });

  // -------------------------------------------------------------------------
  // Lightbox failure path: seeded ai-fixture-failed-1 has a caption
  // failure row. The retry button must POST /api/v1/ai/retry-photo. We
  // assert on the request shape via waitForResponse rather than waiting
  // for the worker to round-trip through the mock VLM and update the DB
  // — that path is timing-dependent in e2e.
  // -------------------------------------------------------------------------
  test("lightbox shows caption failure with retry button", async ({ page }) => {
    if (!AI_PRE_ACKED) {
      await ackHiddenProcessing(page);
    }
    await page.goto("/media/ai-fixture-failed-1?from=library");
    await expect(page.locator(".lb-backdrop")).toBeVisible();
    await page.keyboard.press("i");
    await expect(page.locator(".lb-drawer, .bs-sheet").first()).toBeVisible();

    await expect(page.getByText(/Caption failed/)).toBeVisible();

    const retryBtn = page.getByRole("button", { name: /Retry/ });
    await expect(retryBtn).toBeVisible();

    const responsePromise = page.waitForResponse((resp) =>
      resp.url().includes("/api/v1/ai/retry-photo"),
    );
    await retryBtn.click();
    const resp = await responsePromise;
    expect(resp.status()).toBe(200);
  });
});
