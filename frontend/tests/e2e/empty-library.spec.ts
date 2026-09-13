import { test, expect } from "@playwright/test";

for (const view of ["library", "sessions"]) {
  test(`${view} offers import guidance only after an empty successful read`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    let failing = true;
    await page.route("**/api/v1/media?*", async (route) => {
      if (failing) await route.fulfill({ status: 503, json: { title: "Unavailable" } });
      else await route.fulfill({ json: { items: [] } });
    });
    await page.goto(`/${view}`);
    await expect(page.getByRole("alert")).toContainText("Couldn’t load photos.");
    await expect(page.getByRole("link", { name: "Import guide", exact: true })).toBeHidden();
    failing = false;
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Add photos to your library" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Import guide", exact: true })).toHaveAttribute("href", "https://fotobank.kenn.io/docs/guides/import/");
    await expect(page.getByRole("link", { name: "Setup guide", exact: true })).toHaveAttribute("href", "https://fotobank.kenn.io/docs/guides/setup/");
    await expect(page.locator("code")).toContainText("fotobank import");
    expect(await page.locator(".main").evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  });
}

test("filtered empty results offer clearing filters, not importing", async ({ page }) => {
  await page.route("**/api/v1/media?*", async (route) => {
    if (new URL(route.request().url()).searchParams.has("camera")) await route.fulfill({ json: { items: [] } });
    else await route.continue();
  });
  await page.goto("/library?camera=Missing");
  await expect(page.getByText("No photos match these filters.")).toBeVisible();
  await expect(page.getByRole("link", { name: "Import guide", exact: true })).toBeHidden();
  await page.getByRole("button", { name: "Clear all", exact: true }).click();
  await expect(page.locator("[data-media-id]").first()).toBeVisible();
});

test("sessions does not mistake an empty library filter for an empty library", async ({ page }) => {
  await page.route("**/api/v1/media?*", async (route) => {
    if (new URL(route.request().url()).searchParams.has("camera")) await route.fulfill({ json: { items: [] } });
    else await route.continue();
  });
  await page.goto("/library?camera=Missing");
  await expect(page.getByText("No photos match these filters.")).toBeVisible();
  await page.getByRole("link", { name: "Sessions", exact: true }).click();
  await expect(page.locator("[data-media-id]").first()).toBeVisible();
  await expect(page.getByRole("link", { name: "Import guide", exact: true })).toBeHidden();
});
