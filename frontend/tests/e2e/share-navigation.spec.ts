import { test, expect } from "@playwright/test";

test("keyboard users can open share details and return to the share", async ({ page }) => {
  await page.goto("/shares");
  const open = page.getByRole("button", { name: "Active e2e share", exact: true });
  await open.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator(".drawer")).toBeVisible();
  await expect(page.getByRole("button", { name: "Close drawer" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator(".drawer")).toBeHidden();
  await expect(open).toBeFocused();
});

test("phone share list shows details and actions without horizontal scrolling", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/shares");
  const row = page.getByRole("row").filter({ hasText: "Active e2e share" });
  await expect(row).toBeVisible();
  expect(await page.locator(".main").evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  await row.getByRole("button", { name: "Revoke" }).scrollIntoViewIfNeeded();
  await expect(row.getByRole("button", { name: "Revoke" })).toBeInViewport();
  await row.getByRole("button", { name: "Revoke" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.locator(".drawer")).toBeHidden();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await row.getByRole("button", { name: "Active e2e share", exact: true }).click();
  await expect(page.locator(".drawer")).toBeVisible();
  await expect(page.locator(".drawer dt", { hasText: "Grantee" })).toBeVisible();
});
