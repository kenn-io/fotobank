import { test, expect, type Locator } from "@playwright/test";

async function expectReadableText(locator: Locator, pseudo: string | null = null) {
  await expect(locator).toBeVisible();
  const ratio = await locator.evaluate((element, pseudoElement) => {
    function luminance(color: string) {
      const [r, g, b] = color.match(/[\d.]+/g)!.slice(0, 3).map(Number).map((value) => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      if (r === undefined || g === undefined || b === undefined) {
        throw new Error(`Expected an RGB color, got ${color}`);
      }
      return r * 0.2126 + g * 0.7152 + b * 0.0722;
    }

    const foreground = getComputedStyle(element, pseudoElement).color;
    let backgroundElement: Element | null = element;
    while (backgroundElement) {
      const background = getComputedStyle(backgroundElement).backgroundColor;
      if (background !== "rgba(0, 0, 0, 0)" && background !== "transparent") {
        const light = luminance(foreground);
        const dark = luminance(background);
        return (Math.max(light, dark) + 0.05) / (Math.min(light, dark) + 0.05);
      }
      backgroundElement = backgroundElement.parentElement;
    }
    throw new Error("No painted background found for text");
  }, pseudo);
  expect(ratio).toBeGreaterThanOrEqual(4.5);
}

for (const width of [390, 1440]) {
  test(`helper text and filter counts remain readable at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/library");
    await expectReadableText(page.getByRole("searchbox", { name: "Search" }), "::placeholder");
    if (width === 390) {
      await page.getByRole("button", { name: "Browse & filters", exact: true }).click();
    }
    await expectReadableText(page.locator(".sidebar .group-header").first());
    await expectReadableText(page.getByRole("checkbox", { name: "Sony A7R IV" }).locator(".count"));

    await page.goto("/settings/ai");
    await expectReadableText(page.locator(".inspection .muted"));
  });
}
