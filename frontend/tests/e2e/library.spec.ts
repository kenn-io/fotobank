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
