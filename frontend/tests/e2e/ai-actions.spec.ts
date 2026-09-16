import { test, expect } from "@playwright/test";

test("failed acknowledgement stays visible and can be retried", async ({ page }) => {
  let failing = true;
  await page.route("**/api/v1/ai/health", async (route) => {
    const response = await route.fetch();
    const health = await response.json();
    await route.fulfill({ response, json: { ...health, paused_reason: failing ? "acknowledgement_required" : "" } });
  });
  await page.route("**/api/v1/ai/acknowledge", async (route) => {
    if (failing) await route.fulfill({ status: 503, json: { title: "Unavailable" } });
    else await route.continue();
  });
  await page.goto("/settings/ai");
  const acknowledge = page.getByRole("button", { name: "Acknowledge and start workers" });
  await acknowledge.click();
  await expect(page.getByRole("alert")).toContainText("Couldn’t save your acknowledgement");
  await expect(acknowledge).toBeEnabled();
  failing = false;
  await acknowledge.click();
  await expect(page.locator(".task-card")).toHaveCount(2);
  await expect(page.getByRole("alert")).toBeHidden();
});

test("failed recent-failure reads remain distinguishable from no failures", async ({ page }) => {
  await page.request.post("/api/v1/ai/acknowledge", { data: { kind: "hidden_processing" } });
  let failing = true;
  await page.route("**/api/v1/ai/failures?*", async (route) => {
    if (failing) await route.fulfill({ status: 503, json: { title: "Unavailable" } });
    else await route.continue();
  });
  await page.goto("/settings/ai");
  await expect(page.getByRole("alert")).toContainText("Couldn’t load recent failures.");
  await expect(page.locator(".task-card")).toHaveCount(2);
  failing = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("alert")).toBeHidden();
});

for (const action of ["backfill", "retry-failed"]) {
  test(`${action} reports failure and the successful retry count`, async ({ page }) => {
    await page.request.post("/api/v1/ai/acknowledge", { data: { kind: "hidden_processing" } });
    await page.route("**/api/v1/ai/health", async (route) => {
      const response = await route.fetch();
      const health = await response.json();
      await route.fulfill({ response, json: { ...health, tag: { ...health.tag, failed_active: 2 } } });
    });
    let failing = true;
    await page.route(`**/api/v1/ai/${action}`, async (route) => {
      await route.fulfill(failing
        ? { status: 503, json: { title: "Unavailable" } }
        : { json: { enqueued: 3 } });
    });
    await page.goto("/settings/ai");
    const button = page.locator(".task-card").first().getByRole("button", {
      name: action === "backfill" ? "Backfill all" : "Retry failed",
    });
    await button.click();
    await expect(page.getByRole("alert")).toContainText("Couldn’t queue");
    await expect(button).toBeEnabled();
    failing = false;
    await button.click();
    await expect(page.getByRole("status")).toContainText("Queued 3 photos");
    await expect(page.getByRole("alert")).toBeHidden();
  });
}

test("failed inspection save restores the checkbox; retry survives reload", async ({ page }) => {
  await page.request.put("/api/v1/settings/user/ai.inspection", { data: { value: "false" } });
  let failing = true;
  await page.route("**/api/v1/settings/user/ai.inspection", async (route) => {
    if (failing && route.request().method() === "PUT") {
      await route.fulfill({ status: 503, json: { title: "Unavailable" } });
    } else await route.continue();
  });
  await page.goto("/settings/ai");
  const toggle = page.getByTestId("ai-inspection-toggle");
  await toggle.check();
  await expect(page.getByRole("alert")).toContainText("Couldn’t save AI Inspection");
  await expect(toggle).not.toBeChecked();
  failing = false;
  await toggle.check();
  await expect(page.getByRole("status")).toContainText("AI Inspection enabled");
  await page.reload();
  await expect(toggle).toBeChecked();
});

test("AI task details and actions fit a phone with long identifiers", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.request.post("/api/v1/ai/acknowledge", { data: { kind: "hidden_processing" } });
  await page.route("**/api/v1/ai/health", async (route) => {
    const response = await route.fetch();
    const health = await response.json();
    await route.fulfill({ response, json: {
      ...health, tag: { ...health.tag, active_fingerprint: "model-".repeat(30), pending: 1234567, failed_active: 2 },
    } });
  });
  await page.goto("/settings/ai");
  await expect(page.locator(".task-card")).toHaveCount(2);
  expect(await page.locator(".main").evaluate(e => e.scrollWidth <= e.clientWidth)).toBe(true);
  await page.locator(".model-details summary").first().click();
  expect(await page.locator(".main").evaluate(e => e.scrollWidth <= e.clientWidth)).toBe(true);
  for (const button of await page.locator(".ai-panel button").all()) {
    expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  }
});
