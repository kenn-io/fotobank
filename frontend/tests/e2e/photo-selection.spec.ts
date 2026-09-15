import { test, expect } from "@playwright/test";

for (const route of ["/search?q=beach", "/map?z=12&c=37.7749,-122.4194"]) {
  test(`${route} browses photos without bulk selection controls`, async ({ page }) => {
    await page.goto(route);
    const photo = page.getByRole("link", { name: /^Photo / }).first();
    await expect(photo).toBeVisible();
    await expect(page.getByRole("checkbox", { name: /^Select / })).toHaveCount(0);
    await photo.click();
    await expect(page.getByTestId("lightbox")).toBeVisible();
  });
}

test("Search viewer walks results rather than a prior Library selection", async ({ page }) => {
  await page.goto("/search?q=beach");
  const photos = page.getByRole("link", { name: /^Photo / });
  await expect(photos.nth(2)).toBeVisible();
  const ids = await photos.evaluateAll((links) => links.slice(0, 3).map((link) => link.getAttribute("data-media-id")!));
  await page.getByRole("link", { name: "Library", exact: true }).click();
  for (const id of [ids[0], ids[2]]) {
    await page.getByRole("checkbox", { name: `Select ${id}`, exact: true }).check();
  }
  await expect(page.getByText("2 selected", { exact: true })).toBeVisible();
  await page.getByRole("searchbox", { name: "Search", exact: true }).fill("beach");
  await page.getByRole("searchbox", { name: "Search", exact: true }).press("Enter");
  await expect(page).toHaveURL(/\/search\?q=beach/);
  await page.getByLabel(`Photo ${ids[0]}`, { exact: true }).click();
  await expect(page.getByTestId("lightbox")).toBeVisible();
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(new RegExp(`/media/${ids[1]}\\?from=search`));
});

test("select photos with the keyboard in Library and Sessions", async ({
  page,
}) => {
  for (const route of ["/library", "/sessions"]) {
    await page.goto(route);
    const select = page.getByRole("checkbox", {
      name: "Select gps-fixture-1",
      exact: true,
    });
    await select.focus();
    await page.keyboard.press("Space");
    await expect(page.getByText("1 selected", { exact: true })).toBeVisible();
    await expect(
      page.getByLabel("Photo gps-fixture-1", { exact: true }),
    ).toHaveClass(/selected/);
    await page.getByRole("button", { name: "Done", exact: true }).click();
    await expect(select).not.toBeChecked();
  }
});

test.describe("Touch photo selection", () => {
  test.use({
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
  });
  let albumId: string;

  test.afterEach(async ({ request }) => {
    if (albumId) await request.delete(`/api/v1/albums/${albumId}`);
  });

  test("a small tile beside a panorama still opens the photo", async ({
    page,
  }) => {
    await page.route("**/api/v1/albums/*/media?*", async (route) => {
      const response = await route.fetch();
      const data = await response.json();
      for (const [index, item] of data.items.entries()) {
        item.width = index === 1 ? 1000 : 100;
        item.height = 100;
      }
      await route.fulfill({ response, json: data });
    });
    await page.goto("/albums");
    await page.getByRole("link", { name: /E2E Italy/ }).tap();
    const photo = page.getByRole("link", { name: /^Photo / }).first();
    await expect(photo).toBeVisible();
    const box = await photo.boundingBox();
    expect(box!.width).toBeLessThan(44);
    const href = await photo.getAttribute("href");
    await photo.tap({ timeout: 3000 });
    await expect(page).toHaveURL(new RegExp(`${href}\\?from=album`));
  });

  test("add selected photos to an album and remove one without a keyboard", async ({
    page,
    request,
  }) => {
    const name = `Phone selects ${crypto.randomUUID()}`;
    const created = await request.post("/api/v1/albums", { data: { name } });
    expect(created.ok()).toBe(true);
    const { id } = await created.json();
    albumId = id;
    await page.goto("/library");
    await page
      .getByRole("checkbox", { name: "Select gps-fixture-1", exact: true })
      .tap();
    await page
      .getByRole("checkbox", {
        name: "Select pair-fixture-primary",
        exact: true,
      })
      .tap();
    await expect(page).toHaveURL(/\/library$/);
    await expect(page.getByText("2 selected", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Add to album", exact: true }).tap();
    await page.getByRole("dialog").getByText(name).tap();
    await page.getByRole("button", { name: "Add 2 photos", exact: true }).tap();
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await page.goto(`/albums/${id}`);
    await expect(page.getByLabel(/^Photo /)).toHaveCount(2);
    const select = page.getByRole("checkbox", {
      name: "Select gps-fixture-1",
      exact: true,
    });
    await select.tap();
    await expect(select).toBeChecked();
    await select.tap();
    await expect(select).not.toBeChecked();
    await select.tap();
    const actions = page.getByRole("toolbar", { name: "Selection actions" });
    expect(
      await actions.evaluate((el) => el.scrollWidth <= el.clientWidth),
    ).toBe(true);
    await page
      .getByRole("button", { name: "Remove from this album", exact: true })
      .tap();
    await expect(
      page.getByLabel("Photo gps-fixture-1", { exact: true }),
    ).toHaveCount(0);
    await page.reload();
    await expect(page.getByLabel(/^Photo /)).toHaveCount(1);
    await page.getByLabel("Photo pair-fixture-primary", { exact: true }).tap();
    await expect(page).toHaveURL(/\/media\/pair-fixture-primary\?from=album/);
  });
});
