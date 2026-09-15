import { test, expect } from "@playwright/test";

for (const failure of ["server", "network"] as const) {
  test.describe(`Album actions after a ${failure} failure`, () => {
    let albumId: string;

    test.beforeEach(async ({ page, request }) => {
      const created = await request.post("/api/v1/albums", {
        data: { name: `Weekend selects ${crypto.randomUUID()}` },
      });
      expect(created.ok()).toBe(true);
      albumId = (await created.json()).id;
      const added = await request.post(`/api/v1/albums/${albumId}/media`, {
        data: { media_ids: ["gps-fixture-1", "pair-fixture-primary"] },
      });
      expect(added.ok()).toBe(true);
      await page.goto(`/albums/${albumId}`);
      await expect(page.getByLabel(/^Photo /)).toHaveCount(2);
    });

    test.afterEach(async ({ request }) => {
      await request.delete(`/api/v1/albums/${albumId}`);
    });

    test("reports partial removal and keeps failed photos selected for retry", async ({
      page,
    }) => {
      const failedPath = `**/api/v1/albums/${albumId}/media/pair-fixture-primary`;
      await page.route(failedPath, async (route) => {
        if (failure === "network") await route.abort("failed");
        else
          await route.fulfill({
            status: 503,
            json: { status: 503, title: "Unavailable" },
          });
      });
      await page
        .getByLabel("Photo gps-fixture-1")
        .click({ modifiers: ["Meta"] });
      await page
        .getByLabel("Photo pair-fixture-primary")
        .click({ modifiers: ["Meta"] });
      await page
        .getByRole("button", { name: "Remove from this album" })
        .click();

      const notice = page
        .getByRole("status")
        .filter({ hasText: /could not be removed/i });
      await expect(notice).toBeVisible();
      await expect(notice).toContainText(/still selected.*try again/i);
      await expect(page.getByLabel("Photo gps-fixture-1")).toHaveCount(0);
      await expect(page.getByLabel("Photo pair-fixture-primary")).toHaveClass(
        /selected/,
      );
      await expect(page.getByText("1 selected", { exact: true })).toBeVisible();

      await notice.getByRole("button", { name: "Dismiss" }).click();
      await page.unroute(failedPath);
      await page
        .getByRole("button", { name: "Remove from this album" })
        .click();
      await expect(
        page.getByText("Empty album.", { exact: false }),
      ).toBeVisible();
      await page.reload();
      await expect(
        page.getByText("Empty album.", { exact: false }),
      ).toBeVisible();
    });

    test("reports failed deletion without leaving the album and allows retry", async ({
      page,
    }) => {
      const failedPath = `**/api/v1/albums/${albumId}`;
      await page.route(failedPath, async (route) => {
        if (route.request().method() !== "DELETE") return route.continue();
        if (failure === "network") await route.abort("failed");
        else
          await route.fulfill({
            status: 503,
            json: { status: 503, title: "Unavailable" },
          });
      });
      await page.getByRole("button", { name: "Delete", exact: true }).click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Delete", exact: true })
        .click();

      const notice = page
        .getByRole("status")
        .filter({ hasText: /could not delete/i });
      await expect(notice).toBeVisible();
      await expect(notice).toContainText(/try again/i);
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(page).toHaveURL(new RegExp(`/albums/${albumId}$`));
      await expect(page.getByLabel(/^Photo /)).toHaveCount(2);

      await notice.getByRole("button", { name: "Dismiss" }).click();
      await page.unroute(failedPath);
      await page.getByRole("button", { name: "Delete", exact: true }).click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Delete", exact: true })
        .click();
      await expect(page).toHaveURL(/\/albums$/);
    });
  });
}
