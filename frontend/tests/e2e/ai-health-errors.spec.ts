import { test, expect } from "@playwright/test";

test("AI status failure offers retry instead of endless loading", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let unavailable = true;
  await page.route("**/api/v1/ai/health", async (route) => {
    if (unavailable) await route.fulfill({ status: 503, json: { title: "Unavailable" } });
    else await route.continue();
  });

  await page.goto("/settings/ai");
  await expect(page.getByRole("alert")).toContainText("Couldn’t load AI status.");
  await expect(page.getByText("Loading…", { exact: true })).toBeHidden();
  unavailable = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("alert")).toBeHidden();
  await expect(page.getByText("Loading…", { exact: true })).toBeHidden();
  await expect(page.locator(".ack-modal, .task-card").first()).toBeVisible();
  expect(errors).toEqual([]);
});
