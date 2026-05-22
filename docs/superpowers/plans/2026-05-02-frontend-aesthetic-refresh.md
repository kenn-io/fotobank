# Frontend Aesthetic Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the design captured in [docs/superpowers/specs/2026-05-02-frontend-aesthetic-refresh-design.md](../specs/2026-05-02-frontend-aesthetic-refresh-design.md) — dark-only token system + lightbox/bottom-sheet chrome scrub + monospace metadata, in 4 commits.

**Architecture:** Two layers. The token layer rewrites `frontend/src/app.css` and updates 14 known call sites in the same commit so the SPA never lands in a half-renamed state. The chrome scrub then replaces hardcoded hex/rgba/font-size literals in the components flagged by the visual-foundation survey (mostly lightbox + bottom sheet + modals).

**Tech Stack:** Svelte 5, TypeScript, Vite, vitest, Playwright.

---

### Task 1: Token rewrite + global rename

**Files:**
- Rewrite: `frontend/src/app.css`
- Modify (rename `var(--bg-primary)` → `var(--bg-base)`):
  - `frontend/src/lib/components/ThreeColumnLayout.svelte:34`
  - `frontend/src/lib/components/StickyMonthBar.svelte:20`
- Modify (rename `var(--radius)` → `var(--radius-sm)`):
  - `frontend/src/lib/search/SearchFiltersPopover.svelte`
  - `frontend/src/lib/search/IndexingStatusBanner.svelte`
  - `frontend/src/lib/search/SearchSortSegment.svelte`
  - `frontend/src/lib/components/HiddenLockStrip.svelte`
  - `frontend/src/lib/components/Sidebar.svelte`
  - `frontend/src/lib/components/ToastStack.svelte`
  - `frontend/src/lib/components/HiddenGate.svelte`
  - `frontend/src/lib/components/DensityControl.svelte`
  - `frontend/src/lib/components/AppHeader.svelte`
- Modify (rename `var(--shadow)` → `var(--shadow-sm)`):
  - `frontend/src/lib/components/ToastStack.svelte`
  - `frontend/src/lib/components/AppHeader.svelte`
- Create: `frontend/src/test/app-css-smoke.test.ts`

- [ ] **Step 1: Rewrite `frontend/src/app.css` to the design's token scheme.**

The new file replaces the entire current contents. Source of truth for values is the spec's "Token decisions" section. Concretely:

```css
/* frontend/src/app.css */
:root {
  color-scheme: dark;

  /* Surfaces */
  --bg-base:     #0c0e12;
  --bg-surface:  #14171c;
  --bg-elevated: #1a1e25;
  --bg-hover:    #20242c;
  --bg-overlay:  rgba(0, 0, 0, 0.78);

  /* Text */
  --text-primary:   #e6e9ef;
  --text-secondary: #a4abb6;
  --text-muted:     #6c7480;

  /* Accent */
  --accent:       #3a7bd5;
  --accent-hover: #4a8bea;
  --accent-fg:    #ffffff;

  /* Borders */
  --border:        #232831;
  --border-strong: #2c333d;

  /* Status */
  --danger: #d04545;
  --warn:   #d68a3a;
  --ok:     #4aaa6a;
  --orange: #d97706;

  /* Type */
  --font-ui:   system-ui, -apple-system, "SF Pro Text", "Segoe UI", sans-serif;
  --font-mono: ui-monospace, "JetBrains Mono", "SF Mono", Menlo, monospace;
  --text-xs:   11px;
  --text-sm:   12px;
  --text-base: 13px;
  --text-md:   14px;
  --text-lg:   16px;

  /* Spacing */
  --space-1: 2px;
  --space-2: 4px;
  --space-3: 6px;
  --space-4: 8px;
  --space-5: 12px;
  --space-6: 16px;
  --space-7: 24px;
  --space-8: 32px;

  /* Radii */
  --radius-none: 0;
  --radius-sm:   2px;
  --radius-md:   4px;

  /* Shadows */
  --shadow-none: none;
  --shadow-sm:   0 1px 0 rgba(0, 0, 0, 0.4);
  --shadow-md:   0 8px 24px rgba(0, 0, 0, 0.45);

  font-family: var(--font-ui);
  font-size: var(--text-base);
}

body {
  margin: 0;
  background: var(--bg-base);
  color: var(--text-primary);
}

* { box-sizing: border-box; }
```

The legacy `:root.theme-light`, `:root.theme-dark`, and `@media (prefers-color-scheme: dark)` blocks are gone. `color-scheme: dark` keeps native form controls and scrollbars in dark mode.

- [ ] **Step 2: Run `var(--bg-primary)` rename in two callers.**

```sh
cd frontend && rg -l "var\(--bg-primary\)" src
```

Replace `var(--bg-primary)` with `var(--bg-base)` in `ThreeColumnLayout.svelte:34` and `StickyMonthBar.svelte:20`. The grep above must return zero matches after the edits.

- [ ] **Step 3: Run `var(--radius)` rename across nine files.**

```sh
cd frontend && rg -l "var\(--radius\)" src
```

For each file, replace `var(--radius)` with `var(--radius-sm)`. Confirm `rg "var\(--radius\)" src` returns zero matches. Files in scope:

```
lib/components/AppHeader.svelte
lib/components/DensityControl.svelte
lib/components/HiddenGate.svelte
lib/components/HiddenLockStrip.svelte
lib/components/Sidebar.svelte
lib/components/ToastStack.svelte
lib/search/IndexingStatusBanner.svelte
lib/search/SearchFiltersPopover.svelte
lib/search/SearchSortSegment.svelte
```

- [ ] **Step 4: Run `var(--shadow)` rename across two files.**

```sh
cd frontend && rg -l "var\(--shadow\)" src
```

Replace `var(--shadow)` with `var(--shadow-sm)` in `ToastStack.svelte` and `AppHeader.svelte`. Confirm zero matches afterward.

- [ ] **Step 5: Add the `app.css` smoke test.**

Create `frontend/src/test/app-css-smoke.test.ts` with the contents below. The test imports `app.css` as raw text (Vite's `?raw` query) and asserts the legacy palette / class names are gone. This guards against partial removal — if a future edit accidentally restores `--bg-primary`, the test fails loudly.

```typescript
import { describe, it, expect } from "vitest";
import css from "../app.css?raw";

describe("app.css token surface", () => {
  it("does not declare the removed light-mode class", () => {
    expect(css).not.toMatch(/\.theme-light/);
  });

  it("does not declare a prefers-color-scheme block (dark is the only mode)", () => {
    expect(css).not.toMatch(/prefers-color-scheme/);
  });

  it("does not declare the renamed legacy tokens", () => {
    // --bg-primary / --radius / --shadow were renamed; their absence
    // confirms callers were swept rather than left as orphan refs.
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
```

The `?raw` import requires Vite, which is the test environment — it works in vitest because vitest reuses Vite's plugin pipeline.

- [ ] **Step 6: Run typecheck and unit tests.**

```sh
cd frontend && bun run check && bun run test
```

Both must pass. The smoke test from Step 5 should be among the passing tests; if it fails, the rewrite is incomplete.

- [ ] **Step 7: Commit Task 1.**

```sh
cd /path/to/fotobank
git add frontend/src/app.css \
  frontend/src/lib/components/ThreeColumnLayout.svelte \
  frontend/src/lib/components/StickyMonthBar.svelte \
  frontend/src/lib/components/AppHeader.svelte \
  frontend/src/lib/components/DensityControl.svelte \
  frontend/src/lib/components/HiddenGate.svelte \
  frontend/src/lib/components/HiddenLockStrip.svelte \
  frontend/src/lib/components/Sidebar.svelte \
  frontend/src/lib/components/ToastStack.svelte \
  frontend/src/lib/search/IndexingStatusBanner.svelte \
  frontend/src/lib/search/SearchFiltersPopover.svelte \
  frontend/src/lib/search/SearchSortSegment.svelte \
  frontend/src/test/app-css-smoke.test.ts
git commit -m "feat(frontend/css): aesthetic refresh — new token scheme + global rename

Replaces app.css with the dark-only token system from the aesthetic
refresh spec. Renames --bg-primary → --bg-base, --radius → --radius-sm,
--shadow → --shadow-sm across all 14 known call sites in the same
commit so the SPA never lands in a half-renamed state. Adds an app.css
smoke test asserting the legacy palette/classes are gone."
```

---

### Task 2: Theme + kebab cleanup

**Files:**
- Delete: `frontend/src/lib/theme/themeStore.svelte.ts`
- Delete: `frontend/src/lib/theme/themeStore.test.ts`
- Modify: `frontend/src/App.svelte` (remove ThemeStore instance + AppHeader theme props)
- Modify: `frontend/src/lib/components/AppHeader.svelte` (delete kebab button + dropdown markup + theme state)
- Modify: `frontend/src/lib/components/AppHeader.test.ts` (delete the menu describe blocks; trim helper defaults)

- [ ] **Step 1: Delete `themeStore`.**

```sh
cd /path/to/fotobank
git rm frontend/src/lib/theme/themeStore.svelte.ts frontend/src/lib/theme/themeStore.test.ts
rmdir frontend/src/lib/theme 2>/dev/null || true
```

The `lib/theme/` directory has no other files, so it should be removed. The `rmdir` is best-effort — it's harmless if the directory is already gone.

- [ ] **Step 2: Trim `App.svelte` of ThemeStore.**

In `frontend/src/App.svelte`, remove the import line `import { ThemeStore } from "./lib/theme/themeStore.svelte";`, remove the construction `const themeStore = new ThemeStore(api); themeStore.load();`, and trim the `<AppHeader>` props back to identity only:

```svelte
<AppHeader
  hub={appConfig.principal?.hub}
  handle={appConfig.principal?.handle}
/>
```

The settings placeholder route currently renders `Settings (placeholder; theme = {themeStore.theme})` and breaks typecheck once the store is gone. Replace it with `Settings (placeholder)` in the same edit so this step lands as a clean unit. (Caught by roborev #17080 against an earlier draft of the plan.)

- [ ] **Step 3: Strip the kebab from `AppHeader.svelte`.**

Remove these from `frontend/src/lib/components/AppHeader.svelte`:

- The `import type { Theme } from "../theme/themeStore.svelte";` line.
- The `theme` and `onSetTheme` props from the `$props()` destructure (and the type union).
- The `menuOpen`, `menuEl`, click-outside `$effect`, and `chooseTheme` function.
- The entire `<div class="account-menu">…</div>` block.
- The `.account-menu`, `.account`, `.account-dropdown`, `.dropdown-section-label`, `.dropdown-item`, `.dropdown-item:hover`, `.dropdown-item.active` style rules.

The header retains: brand, identity, search, AIStatusDot, plus its existing search-related effects.

- [ ] **Step 4: Trim `AppHeader.test.ts`.**

Delete the `describe("AppHeader account menu", …)` block and the `aria-checked` test it contains. Trim `renderHeader`'s default props to remove `theme` and `onSetTheme`. The `describe("AppHeader identity display", …)` block stays. The original search-input describe block stays.

The trimmed helper:

```typescript
function renderHeader(overrides: Record<string, unknown> = {}) {
  return render(AppHeader, {
    props: {
      hub: "dev-local",
      handle: "owner",
      ...overrides,
    },
  });
}
```

The earlier search-input test that focuses `button.account` to prove ⌘K shifts focus needs to focus a different element since the button is gone. Replace:

```typescript
const button = container.querySelector("button.account") as HTMLButtonElement;
button.focus();
expect(document.activeElement).toBe(button);
```

with:

```typescript
// Move focus elsewhere first so we can prove ⌘K shifts it back. The
// brand div isn't focusable; the search input itself is what we want
// to assert focus on, so steal focus to document.body explicitly.
(document.activeElement as HTMLElement | null)?.blur();
expect(document.activeElement).toBe(document.body);
```

- [ ] **Step 5: Run typecheck and unit tests.**

```sh
cd frontend && bun run check && bun run test
```

If any test outside the touched files fails, investigate before proceeding — it likely means another component imported `themeStore` and the deletion broke them. None should today (verified via `rg -l themeStore frontend/src` before this task), but verify.

- [ ] **Step 6: Commit Task 2.**

```sh
cd /path/to/fotobank
git add -A
git commit -m "refactor(frontend): drop themeStore and kebab menu (dark-only)

Aesthetic-refresh task 2. The SPA is dark-only now per the spec, so
the theme store and the kebab dropdown that surfaced theme switching
have no reason to exist. The kebab button itself is also removed —
there's no second item to put behind it today, and exposing the build
version would breach the no-backend-changes scope of this pass.

Backend /api/v1/settings/user/{key} stays. The 'theme' key is now
unused; future settings (density, default sort) will reuse it."
```

---

### Task 3: Chrome scrub + metadata monospace

**Files (per spec's chrome scrub table):**
- Modify: `frontend/src/lib/components/lightbox/LightboxFrame.svelte`
- Modify: `frontend/src/lib/components/lightbox/LightboxToolbar.svelte`
- Modify: `frontend/src/lib/components/lightbox/LightboxNavButtons.svelte`
- Modify: `frontend/src/lib/components/lightbox/LightboxMedia.svelte`
- Modify: `frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte`
- Modify: `frontend/src/lib/components/lightbox/LightboxInfoDrawer.svelte`
- Modify: `frontend/src/lib/components/lightbox/LightboxMetadata.svelte` (also gets the monospace pass)
- Modify: `frontend/src/lib/components/BottomSheet.svelte`
- Modify: `frontend/src/lib/components/AppHeader.svelte` (search radius and padding)
- Modify: `frontend/src/lib/components/ToastStack.svelte` (verify token usage)
- Modify: `frontend/src/lib/components/ConfirmModal.svelte`
- Modify: `frontend/src/lib/components/AddToAlbumModal.svelte`
- Modify: `frontend/src/lib/components/ShareModal.svelte`

- [ ] **Step 1: Per-file scrub procedure.**

For each file in the list above, follow this procedure exactly:

1. Open the file's `<style>` block.
2. Run these greps inside the file's contents (mentally or via search):
   - `#[0-9a-fA-F]{3,8}\b` — hex literals
   - `rgba?\(` — rgba/rgb literals
   - `\b\d+px\b` followed by a font-size context — hardcoded sizes
3. Replace each match with the closest token:
   - Background hexes / rgba on overlays → `var(--bg-overlay)`, `var(--bg-surface)`, `var(--bg-elevated)`, `var(--bg-hover)`, or `var(--bg-base)` based on visual layer.
   - Text hexes → `var(--text-primary)`, `var(--text-secondary)`, `var(--text-muted)`.
   - Border hexes / rgba → `var(--border)` or `var(--border-strong)`.
   - Accent hexes → `var(--accent)`.
   - Hardcoded radii → `var(--radius-sm)` (default) or `var(--radius-md)` (modals/dropdowns). The bottom sheet's 16px top radius drops to 0 entirely (per spec).
   - Hardcoded font sizes → `var(--text-xs|sm|base|md|lg)` matching the closest existing pixel size.
   - Hardcoded padding/gap values → token from the spacing scale `var(--space-N)` when there's a clean match; leave alone if the pixel value doesn't fit any scale stop (e.g. `8px`/`12px`/`16px` map cleanly; `7px` does not — keep `7px` rather than fudge).
4. After edits, the file's `<style>` block should have zero hex literals and zero `rgba(` literals (except where the design genuinely needs alpha — e.g. an `--bg-overlay` reference).

Do every file in this task in a single commit. Do not split mid-scrub.

- [ ] **Step 2: Apply monospace to `LightboxMetadata.svelte` numeric values.**

In `LightboxMetadata.svelte`, the `<dl>` renders label/value pairs. Add a style rule scoped to the value cells that should read in monospace:

```svelte
<style>
  /* … existing styles, now using tokens … */

  dd.numeric {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    color: var(--text-primary);
  }
</style>
```

Then in the template, mark the `<dd>` for numeric/technical fields with `class="numeric"`. The numeric fields per the spec are: ISO, shutter speed, aperture, focal length, file size, dimensions, latitude/longitude. Non-numeric metadata (camera make/model, location label, description, capture timestamp) stays in the UI font.

If `LightboxMetadata.svelte` doesn't currently have a `<dl>` shape (it might use divs), apply the equivalent class on whatever wrapper holds the numeric value.

- [ ] **Step 3: AppHeader specific tweaks.**

Beyond the rename already done in Task 1, the search input still has `border-radius: 14px` (a pill shape that reads web-app-y). Change it to `var(--radius-sm)`. Padding values `6px 12px` become `var(--space-3) var(--space-5)` for the strip; the search input's `0 10px` becomes `0 var(--space-4)`.

- [ ] **Step 4: Verify the scrub is complete.**

Run BOTH greps over the same target list — hex literals AND rgba/rgb literals. The original draft of this plan only checked hex; rgba leftovers (the lightbox toolbar's old `rgba(0,0,0,0.5)` etc.) could pass that one verification. (Caught by roborev #17080.)

```sh
(
  cd frontend || exit 1
  SCRUB_TARGETS="src/lib/components/lightbox \
    src/lib/components/BottomSheet.svelte \
    src/lib/components/ConfirmModal.svelte \
    src/lib/components/AddToAlbumModal.svelte \
    src/lib/components/ShareModal.svelte \
    src/lib/components/ToastStack.svelte \
    src/lib/components/AppHeader.svelte"
  rg "#[0-9a-fA-F]{3,8}\b" $SCRUB_TARGETS
  rg "rgba?\(" $SCRUB_TARGETS
)
```

Both greps should return zero or near-zero matches. Any remaining hits must have a comment explaining why (e.g. `transparent` is fine, an SVG fill that has to be inline, an `rgba(...)` declaration that's the underlying value of `var(--bg-overlay)` itself, etc.).

- [ ] **Step 5: Run typecheck and unit tests.**

```sh
cd frontend && bun run check && bun run test
```

All tests must pass. If a Lightbox test fails because of asserted text/structure changes, investigate — the scrub should not change what the user sees beyond styling.

- [ ] **Step 6: Commit Task 3.**

```sh
cd /path/to/fotobank
git add frontend/src/lib/components/lightbox \
  frontend/src/lib/components/BottomSheet.svelte \
  frontend/src/lib/components/AppHeader.svelte \
  frontend/src/lib/components/ToastStack.svelte \
  frontend/src/lib/components/ConfirmModal.svelte \
  frontend/src/lib/components/AddToAlbumModal.svelte \
  frontend/src/lib/components/ShareModal.svelte
git commit -m "feat(frontend/css): chrome scrub — lightbox, modals, monospace metadata

Aesthetic-refresh task 3. Replaces hardcoded hex/rgba/font-size
literals across the lightbox chrome, bottom sheet, modals, toasts,
and AppHeader search input with the new token surface from task 1.
LightboxMetadata's numeric/technical values (ISO, shutter, aperture,
focal length, dimensions, file size, lat/lon) now render in
monospace with tabular-nums for column alignment.

Bottom sheet's 16px top-rounded corners are gone (square edges per
spec); modal radii standardize on --radius-md (4px); button radii
use --radius-sm (2px)."
```

---

### Task 4: Verification

**Files:** none modified — this task only runs checks and records the QA notes.

- [ ] **Step 1: Rebuild the binary.**

```sh
cd /path/to/fotobank
make build
```

`make build` rebuilds the SPA into `internal/web/dist/` and embeds it into the Go binary. Required so the running server picks up the visual changes.

- [ ] **Step 2: Run the full test suite.**

```sh
cd frontend
bun run check
bun run test
```

```sh
cd /path/to/fotobank
make test
```

All three (svelte-check, vitest, go test) must pass.

- [ ] **Step 3: Run the e2e suite.**

```sh
cd /path/to/fotobank/frontend
bun run test:e2e
```

The map and lightbox tests are the most likely to surface regressions since they exercise the deepest chrome. If a test fails for a reason that isn't a real regression (e.g. an assertion on an exact pixel value) flag it for the user before patching the test.

- [ ] **Step 4: Manual QA across the major routes.**

Restart the running server (`bin/fotobank server`) and walk through these routes with at least one imported photo, confirming the visual feel matches the spec. Record observations in the commit message:

- `/library` — grid density, sticky month bar, empty state if no photos.
- `/sessions` — same grid, different grouping.
- `/map` — Leaflet container, markers, mobile tabs.
- `/albums` and one `/albums/<id>` — index list, album header, member grid.
- `/search` — search bar, filters popover, results.
- `/hidden` — gate UI, library after unlock.
- Lightbox (open any photo from library) — backdrop, toolbar, nav arrows, info drawer (←→ to walk, `i` to toggle), monospace metadata alignment.
- AppHeader — identity reads correctly (`dev-local: owner` for the local config), search input reads as compact rectangle (no pill).

For each route, note: ✅ matches expected feel, or ⚠ deviation (with one-line description).

- [ ] **Step 5: Commit Task 4 with the QA log.**

```sh
cd /path/to/fotobank
git commit --allow-empty -m "chore(frontend/css): aesthetic refresh QA log — manual verification

Manual QA across /library, /sessions, /map, /albums, /search, /hidden,
and lightbox. Test suites all green: svelte-check, vitest (N tests),
playwright (M tests), go test ./...

Per-route notes:
- /library: <observation>
- /sessions: <observation>
- /map: <observation>
- /albums: <observation>
- /search: <observation>
- /hidden: <observation>
- lightbox: <observation>
- AppHeader: <observation>

Closes the aesthetic refresh spec. Density toggles, empty-state
visuals, and a custom font face remain as future work per the
non-goals section of the spec."
```

The implementer should fill in the `<observation>` lines and `N`/`M` test counts before committing. An empty commit is acceptable here — the QA log itself is the artifact.

---

## Self-review

Spec coverage: every section of the spec is addressed by a task — tokens (Task 1), theme cleanup (Task 2), chrome scrub + monospace (Task 3), verification (Task 4). The "removed/renamed in app.css" inventory in the spec maps directly onto Task 1 Steps 1–4. The "chrome scrub targets" table maps onto Task 3 Step 1's per-file procedure. The "test strategy" section maps onto Task 1 Step 5 (smoke test) and Task 4 Steps 2–3.

Placeholder scan: the Task 4 commit message has explicit `<observation>` placeholders that the implementer fills in based on actual QA — these are not plan placeholders, they're a template for the QA log. No "TBD"/"TODO" or "implement later" anywhere. Step procedures are concrete (greps, exact replacements, exact file paths).

Type consistency: token names are consistent across spec and plan. `--bg-base` (not `--bg-primary`), `--radius-sm` (not `--radius`), `--shadow-sm` (not `--shadow`), `--font-mono` (not `--mono-font`). The smoke test from Task 1 Step 5 asserts these names; if a later task references a different name, the smoke test catches it.
