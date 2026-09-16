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

  it("maps the darkroom palette onto kit-ui tokens", () => {
    expect(css).toMatch(/--bg-primary:\s*#0a0a0d/);
    expect(css).toMatch(/--bg-surface:\s*#14141a/);
    expect(css).toMatch(/--bg-inset:\s*#1d1d24/);
    expect(css).toMatch(/--text-primary:\s*#ecebe6/);
    expect(css).toMatch(/--text-secondary:\s*#99968d/);
    expect(css).toMatch(/--accent-blue:\s*#e8a44b/);
    expect(css).toMatch(/--fb-accent-deep:\s*#c98935/);
    expect(css).toMatch(/--border-default:\s*#25252c/);
    expect(css).toMatch(/--border-muted:\s*#2e2e36/);
  });

  it("declares the darkroom typography tokens", () => {
    expect(css).toMatch(/--font-mono:\s*"IBM Plex Mono"/);
    expect(css).toMatch(/--font-sans:\s*"IBM Plex Sans"/);
    expect(css).toMatch(/--fb-font-display:\s*"Fraunces"/);
    expect(css).toMatch(/--letter-spacing-label:\s*0\.085em/);
  });

  it("self-hosts the Plex Sans font face", () => {
    expect(css).toMatch(/@font-face\s*\{[^}]*"IBM Plex Sans"/);
  });

  it("renders the body grain overlay", () => {
    expect(css).toMatch(/body::before/);
  });
});
