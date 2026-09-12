import { test, expect } from "@playwright/test";

test.use({ viewport: { width: 390, height: 844 } });

test("phone navigation gives photos the screen and keeps filters accessible", async ({ page }) => {
  await page.goto("/library");
  const browse = page.getByRole("button", { name: "Browse & filters" });
  await expect(browse).toBeVisible();
  await expect(page.locator(".sidebar")).toBeHidden();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  expect((await page.locator(".main").boundingBox())!.width).toBeGreaterThan(360);

  await browse.click();
  await expect(page.getByRole("link", { name: "Albums", exact: true })).toBeVisible();
  await expect(page.getByText("Cameras", { exact: true })).toBeVisible();
  await page.getByRole("checkbox", { name: "Sony A7R IV" }).click();
  await expect(page).toHaveURL(/camera=/);
  await expect(page.locator(".sidebar")).toBeVisible();
  await page.getByRole("button", { name: "Close browse & filters" }).click();
  await expect(page.locator(".sidebar")).toBeHidden();

  await browse.click();
  await page.keyboard.press("Escape");
  await expect(page.locator(".sidebar")).toBeHidden();
  await expect(browse).toBeFocused();

  await browse.click();
  await page.getByRole("link", { name: "Albums", exact: true }).click();
  await expect(page).toHaveURL(/\/albums$/);
  await expect(page.getByRole("button", { name: "New album" })).toBeVisible();
  await expect(page.locator(".sidebar")).toBeHidden();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(page.locator(".sidebar")).toBeVisible();
  await expect(browse).toBeHidden();
});
