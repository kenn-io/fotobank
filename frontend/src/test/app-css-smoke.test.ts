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
    expect(css).not.toMatch(/--bg-base\s*:/);
    expect(css).not.toMatch(/--bg-surface\s*:/);
    expect(css).not.toMatch(/--bg-elevated\s*:/);
    expect(css).not.toMatch(/--bg-hover\s*:/);
    expect(css).not.toMatch(/--bg-overlay\s*:/);
    expect(css).not.toMatch(/--text-primary\s*:/);
    expect(css).not.toMatch(/--text-secondary\s*:/);
    expect(css).not.toMatch(/--text-muted\s*:/);
    expect(css).not.toMatch(/--accent\s*:/);
    expect(css).not.toMatch(/--accent-fg\s*:/);
    expect(css).not.toMatch(/--accent-hover\s*:/);
    expect(css).not.toMatch(/--border-strong\s*:/);
    expect(css).not.toMatch(/--radius-sm\s*:/);
    expect(css).not.toMatch(/--radius-md\s*:/);
  });

  it("declares the new darkroom palette tokens", () => {
    expect(css).toMatch(/--bg:\s*#0a0a0d/);
    expect(css).toMatch(/--surface:\s*#14141a/);
    expect(css).toMatch(/--surface-2:\s*#1d1d24/);
    expect(css).toMatch(/--ink:\s*#ecebe6/);
    expect(css).toMatch(/--ink-2:\s*#99968d/);
    expect(css).toMatch(/--ink-3:\s*#5a5751/);
    expect(css).toMatch(/--amber:\s*#e8a44b/);
    expect(css).toMatch(/--amber-deep:\s*#c98935/);
    expect(css).toMatch(/--border:\s*#25252c/);
    expect(css).toMatch(/--border-2:\s*#2e2e36/);
  });

  it("declares the darkroom typography tokens", () => {
    expect(css).toMatch(/--font-mono:\s*"IBM Plex Mono"/);
    expect(css).toMatch(/--font-ui:\s*"IBM Plex Sans"/);
    expect(css).toMatch(/--font-display:\s*"Fraunces"/);
    expect(css).toMatch(/--label-track:\s*0\.085em/);
  });

  it("self-hosts the Plex Sans font face", () => {
    expect(css).toMatch(/@font-face\s*\{[^}]*"IBM Plex Sans"/);
  });

  it("renders the body grain overlay", () => {
    expect(css).toMatch(/body::before/);
  });
});
