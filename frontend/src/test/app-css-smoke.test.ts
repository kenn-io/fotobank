import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, it, expect } from "vitest";

// Vite's `?raw` suffix is not handled by vitest's transform pipeline
// for CSS files (the CSS plugin reduces app.css to an empty module),
// so we read the file off disk directly. __dirname is unavailable in
// ESM, hence the import.meta.url dance.
const __dirname = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(__dirname, "../app.css"), "utf8");

describe("app.css token surface", () => {
  it("does not declare the removed light-mode class", () => {
    expect(css).not.toMatch(/\.theme-light/);
  });

  it("does not declare a prefers-color-scheme block (dark is the only mode)", () => {
    expect(css).not.toMatch(/prefers-color-scheme/);
  });

  it("does not declare the renamed legacy tokens", () => {
    expect(css).not.toMatch(/--bg-primary\s*:/);
    expect(css).not.toMatch(/--radius\s*:/);
    expect(css).not.toMatch(/--shadow\s*:/);
  });

  it("declares the new core tokens", () => {
    expect(css).toMatch(/--bg-base\s*:/);
    expect(css).toMatch(/--bg-overlay\s*:/);
    expect(css).toMatch(/--accent\s*:/);
    expect(css).toMatch(/--radius-sm\s*:/);
    expect(css).toMatch(/--shadow-sm\s*:/);
    expect(css).toMatch(/--font-mono\s*:/);
  });
});
