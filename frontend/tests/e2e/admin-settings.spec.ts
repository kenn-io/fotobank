import { test, expect } from "@playwright/test";

test.describe("admin AI settings", () => {
  test("admin can open settings, probe embed, and see embed apply confirmation", async ({ page }) => {
    await page.goto("/settings/ai");
    await page.getByRole("link", { name: /Configure AI/i }).click();
    await expect(page).toHaveURL(/\/admin\/settings\/ai$/);
    await expect(page.getByRole("heading", { name: "AI Settings" })).toBeVisible();

    const embed = page.locator("article").filter({ has: page.getByRole("heading", { name: "Embed" }) });
    await expect(embed).toBeVisible();
    await embed.getByRole("button", { name: "Test" }).click();
    await expect(embed.locator(".probe")).toContainText("ok");

    await embed.getByLabel("Model").fill("siglip2-next");
    await embed.getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("dialog", { name: /Create a new embedding generation/i })).toBeVisible();
    await expect(page.getByText(/old generation stays active/i)).toBeVisible();
    await expect(page.getByText(/siglip2-next/)).toBeVisible();

    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog", { name: /Create a new embedding generation/i })).toHaveCount(0);
  });
});
