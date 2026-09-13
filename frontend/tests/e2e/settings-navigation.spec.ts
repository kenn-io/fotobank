import { test, expect } from "@playwright/test";

for (const width of [1440, 390]) {
  test(`settings is reachable from navigation at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/library");
    if (width === 390) await page.getByRole("button", { name: "Browse & filters", exact: true }).click();
    await page.getByRole("link", { name: "Settings", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Import photos", exact: true })).toHaveAttribute("href", "https://fotobank.kenn.io/docs/guides/import/");
    await page.getByRole("link", { name: "AI processing", exact: true }).click();
    await expect(page).toHaveURL(/\/settings\/ai$/);
    await expect(page.getByRole("heading", { name: "AI", exact: true })).toBeVisible();
    await page.goBack();
    await page.getByRole("link", { name: "Hidden photos", exact: true }).click();
    await expect(page).toHaveURL(/\/hidden$/);
  });
}
