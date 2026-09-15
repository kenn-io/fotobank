import { test, expect } from "@playwright/test";

test("library route renders shell + sidebar", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("fotobank")).toBeVisible();
  // Two surfaces now expose a "Library" link — the AppHeader nav and the
  // Sidebar BROWSE group. The original test predates the AppHeader nav;
  // scope to the sidebar to assert sidebar mounts (the brand text above
  // already proves the AppHeader is up).
  await expect(
    page.getByRole("complementary").getByRole("link", { name: "Library" }),
  ).toBeVisible();
});

test("sessions renders a ready thumbnail", async ({ page }) => {
  await page.goto("/sessions");
  const photo = page.getByLabel("Photo search-fixture-vis-030", { exact: true });
  await photo.scrollIntoViewIfNeeded();
  const thumbnail = photo.locator("img");
  await expect(thumbnail).toBeVisible();
  await expect.poll(() => thumbnail.evaluate(
    (img: HTMLImageElement) => img.complete && img.naturalWidth > 0,
  )).toBe(true);
});

test("user_settings persists arbitrary key/value", async ({ page }) => {
  // End-to-end smoke for /api/v1/settings/user/{key}. The SPA no longer
  // reads a "theme" key (densityStore + inspectionStore exercise the
  // endpoint with real consumers in unit tests); this probe just
  // confirms the persistence surface round-trips an opaque JSON value.
  await page.goto("/");
  const put = await page.request.put("/api/v1/settings/user/e2e.smoke", {
    data: { value: '"ok"' },
  });
  expect(put.status()).toBe(204);
  const get = await page.request.get("/api/v1/settings/user/e2e.smoke");
  expect(get.status()).toBe(200);
  expect(await get.json()).toMatchObject({ value: '"ok"' });
});

test("reload /media/<id> returns SPA shell + matched route", async ({ page }) => {
  await page.goto("/media/abc-123");
  await expect(page.getByText("fotobank")).toBeVisible();
  // The MediaDetail stub renders the back link unconditionally.
  await expect(page.getByRole("link", { name: /back to library/i })).toBeVisible();
});

test("reload /foo-not-a-route renders the SPA shell with NotFound", async ({ page }) => {
  const resp = await page.goto("/foo-not-a-route");
  expect(resp?.status()).toBe(200);
  // exact: true so we match only the AppHeader brand and not the
  // NotFound copy "...any view in fotobank." which would trip strict mode.
  await expect(page.getByText("fotobank", { exact: true })).toBeVisible();
  await expect(page.getByText(/page not found/i)).toBeVisible();
});

test("reload /api/v1/healthz returns API JSON, not the SPA shell", async ({ page }) => {
  const resp = await page.request.get("/api/v1/healthz");
  expect(resp.status()).toBe(200);
  const text = await resp.text();
  // SPA shell would contain <html>; the healthz handler returns JSON.
  expect(text.toLowerCase()).not.toContain("<html");
});

test("SPA nav library→sessions→back does not re-issue the initial media fetch", async ({ page }) => {
  // Track every /api/v1/media list request. Pagination sentinels (offset>0)
  // are permitted and excluded from this count; the contract is "don't
  // re-issue the offset=0 page on SPA nav".
  const initialPageCalls: string[] = [];
  page.on("request", (req) => {
    const url = new URL(req.url());
    if (url.pathname !== "/api/v1/media") return;
    if (url.searchParams.get("offset") !== "0") return;
    initialPageCalls.push(req.url());
  });

  // Wait for the actual /api/v1/media?offset=0 response AND its body —
  // page.waitForResponse only resolves on headers, so the frontend
  // could still be parsing the JSON when we measure the baseline.
  // Combined with a UI signal that the empty-state has rendered, we
  // know MediaStore.loading has cleared before we count fetches.
  const initialResponse = page.waitForResponse(
    (resp) => {
      const url = new URL(resp.url());
      return url.pathname === "/api/v1/media" && url.searchParams.get("offset") === "0";
    },
    { timeout: 5_000 },
  );
  await page.goto("/library");
  const resp = await initialResponse;
  await resp.finished();
  // Wait for at least one seeded MediaCell to render — only happens
  // after MediaStore has finished loading and committed the page into
  // state. MediaCell sets aria-label="Photo <id>" so the seeded
  // gps-fixture-1 row is a deterministic sync signal.
  await expect(page.getByLabel("Photo gps-fixture-1")).toBeVisible();
  const baseline = initialPageCalls.length;
  expect(baseline).toBeGreaterThanOrEqual(1);

  await page.getByRole("link", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/\/sessions$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/library$/);

  // The hoisted MediaStore must NOT have re-fetched offset=0 on either
  // hop; pagination sentinels (offset>0) are filtered out above.
  expect(initialPageCalls.length).toBe(baseline);
});

test("MediaDetail back link SPA-routes to /library without a document fetch", async ({ page }) => {
  // The back arrow on /media/:id MUST go through handleInternalLinkClick
  // so the hoisted MediaStore survives. A naked anchor would issue a
  // top-level document request — track those (resourceType === "document")
  // since framenavigated fires for pushState too and can't distinguish.
  const docRequests: string[] = [];
  page.on("request", (req) => {
    if (req.resourceType() === "document") docRequests.push(req.url());
  });

  await page.goto("/media/abc-123");
  await expect(page.getByRole("link", { name: /back to library/i })).toBeVisible();
  const beforeBack = docRequests.length;
  expect(beforeBack).toBeGreaterThanOrEqual(1); // the page.goto itself

  await page.getByRole("link", { name: /back to library/i }).click();
  await expect(page).toHaveURL(/\/library$/);

  // No new document request should fire from the back-click — only
  // history.pushState. handleInternalLinkClick must have called
  // preventDefault().
  expect(docRequests.length).toBe(beforeBack);
});

test("MediaDetail shows location label when row has GPS", async ({ page }) => {
  // gps-fixture-1 is seeded by cmd/e2e-server with Paris coords +
  // a France-shaped label, so the Location dl row + formatted coords
  // both render.
  await page.goto("/media/gps-fixture-1");
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByText("Location")).toBeVisible();
  await expect(page.getByText(/Paris.*France/)).toBeVisible();
  await expect(page.getByText("48.8566° N, 2.3522° E")).toBeVisible();
});

test("MediaDetail hides location row when row has no GPS", async ({ page }) => {
  // no-gps-fixture-1 is seeded by cmd/e2e-server without lat/lon/label,
  // so the Location dl row should not render at all. The back link
  // renders unconditionally — even during the loading state — so wait
  // for the actual /api/v1/media/<id> response AND the disappearance of
  // the "Loading…" copy before checking that Location is absent.
  // Without this guard the negative assertion can pass mid-load.
  const detailResponse = page.waitForResponse(
    (resp) =>
      resp.url().endsWith("/api/v1/media/no-gps-fixture-1") &&
      resp.status() === 200,
    { timeout: 5_000 },
  );
  await page.goto("/media/no-gps-fixture-1");
  await expect(page.getByText("fotobank")).toBeVisible();
  await (await detailResponse).finished();
  await expect(page.getByText(/loading…/i)).toHaveCount(0);
  await expect(page.getByText("Location")).not.toBeVisible();
});

test("MediaDetail shows the files in an asset", async ({ page }) => {
  const detailResponse = page.waitForResponse(
    (resp) =>
      resp.url().endsWith("/api/v1/media/pair-fixture-primary") &&
      resp.status() === 200,
    { timeout: 5_000 },
  );
  await page.goto("/media/pair-fixture-primary");
  await expect(page.getByText("fotobank")).toBeVisible();
  await (await detailResponse).finished();
  await expect(page.getByText("Files")).toBeVisible();
  await expect(page.getByRole("link", { name: "IMG_1.JPG" })).toBeVisible();
  const rawFile = page.getByRole("link", { name: "IMG_1.DNG" });
  await expect(rawFile).toBeVisible();
  await expect(rawFile).toHaveAttribute(
    "href",
    /\/api\/v1\/media\/pair-fixture-primary\/files\/.+\/content/,
  );
});
