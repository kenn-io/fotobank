import { test, expect } from "@playwright/test";

test("search keeps loaded photos and places page retry at the end of the results", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  let failing = true;
  await page.route("**/api/v1/search?*", async (route) => {
    if (new URL(route.request().url()).searchParams.has("cursor")) {
      if (failing) await route.fulfill({ status: 503, contentType: "application/json", body: '{"title":"Unavailable"}' });
      else await route.fulfill({ json: { results: [], has_more: false, next_cursor: null, effective_sort: "newest", embedding_completeness: 1, semantic_unavailable: false, semantic_unavailable_reason: "" } });
      return;
    }
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, has_more: true, next_cursor: "next-page" } });
  });
  await page.goto("/search?q=photo");
  await expect(page.locator("[data-media-id]").first()).toBeVisible();
  await page.locator(".main").evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await expect(page.getByRole("alert")).toContainText("Couldn’t load more search results.");
  await page.locator(".main").evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await expect(page.getByRole("button", { name: "Retry", exact: true })).toBeInViewport();
  const ids = await page.locator("[data-media-id]").evaluateAll((elements) => elements.map((element) => element.getAttribute("data-media-id")));
  expect(ids.length).toBeGreaterThan(0);
  failing = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("alert")).toBeHidden();
  expect(await page.locator("[data-media-id]").evaluateAll((elements) => elements.map((element) => element.getAttribute("data-media-id")))).toEqual(ids);
});

for (const view of ["library", "search"]) {
  test(`${view} distinguishes a failed read from empty results and retries in place`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const endpoint = view === "library" ? "media" : "search";
    let failing = true;
    const requests: string[] = [];
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await page.route(`**/api/v1/${endpoint}?*`, async (route) => {
      requests.push(route.request().url());
      if (failing) await route.fulfill({ status: 503, contentType: "application/json", body: '{"title":"Unavailable"}' });
      else await route.continue();
    });
    await page.goto(view === "library" ? "/library?facet_tag=beach" : "/search?q=photo");
    await expect(page.getByRole("alert")).toContainText("Couldn’t load");
    await expect(page.getByText("No photos yet.")).toBeHidden();
    await expect(page.getByTestId("search-empty-state")).toBeHidden();
    await expect(page.getByText("0% indexed", { exact: true })).toBeHidden();
    const url = page.url();
    const failedRequest = requests.at(-1);
    failing = false;
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.getByRole("alert")).toBeHidden();
    await expect(page.locator("[data-media-id]").first()).toBeVisible();
    expect(page.url()).toBe(url);
    expect(requests).toContain(failedRequest);
    expect(requests.filter((request) => request === failedRequest).length).toBeGreaterThanOrEqual(2);
    expect(pageErrors).toEqual([]);
  });
}
