import { test, expect } from "@playwright/test";

test("canceling share revocation returns to the drawer action", async ({ page }) => {
  await page.goto("/shares");
  await page.getByText("Active e2e share", { exact: true }).click();
  const drawer = page.locator(".drawer");
  const revoke = drawer.getByRole("button", { name: "Revoke", exact: true });
  await revoke.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("dialog")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(drawer).toBeVisible();
  await expect(revoke).toBeFocused();
  await page.keyboard.press("Enter");
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await expect(revoke).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(drawer).toHaveCount(0);
});

test("album confirmation contains keyboard focus and restores its trigger", async ({ page }) => {
  await page.goto("/albums");
  await page.getByRole("button", { name: "New album" }).click();
  await page.getByLabel("Name").fill(`Keyboard album ${Date.now()}`);
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByText(/^Keyboard album /).click();
  const trigger = page.getByRole("button", { name: "Delete", exact: true });
  await trigger.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(dialog.getByRole("button", { name: "Delete", exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(dialog.getByRole("button", { name: "Delete", exact: true })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();

  await page.keyboard.press("Enter");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Enter");
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();

  let finish!: () => void;
  const held = new Promise<void>((resolve) => { finish = resolve; });
  await page.route("**/api/v1/albums/*", async (route) => {
    if (route.request().method() === "DELETE") await held;
    await route.continue();
  });
  await page.keyboard.press("Enter");
  await dialog.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(dialog.getByRole("button", { name: "Working…" })).toBeDisabled();
  await page.keyboard.press("Tab");
  await expect(dialog).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeVisible();
  finish();
  await expect(page).toHaveURL(/\/albums$/);
});
