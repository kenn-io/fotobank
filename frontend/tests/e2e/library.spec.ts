import { test, expect } from "@playwright/test";

test("library route renders shell + sidebar + empty-state", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByRole("link", { name: "Library" })).toBeVisible();
  await expect(page.getByText(/no photos yet/i)).toBeVisible();
});

test("sessions route renders", async ({ page }) => {
  await page.goto("/sessions");
  await expect(page.getByText("fotobank")).toBeVisible();
});

test("theme override persists via user_settings", async ({ page }) => {
  await page.goto("/settings");
  const put = await page.request.put("/api/v1/settings/user/theme", {
    data: { value: '"dark"' },
  });
  expect(put.status()).toBe(204);
  const get = await page.request.get("/api/v1/settings/user/theme");
  expect(get.status()).toBe(200);
  expect(await get.json()).toMatchObject({ value: '"dark"' });
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
  // Wait for the empty-state copy — only renders after MediaStore has
  // finished loading and committed the (empty) page into state.
  await expect(page.getByText(/no photos yet/i)).toBeVisible();
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
