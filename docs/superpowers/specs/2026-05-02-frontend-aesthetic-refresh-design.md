# Frontend Aesthetic Refresh — Design

**Date:** 2026-05-02
**Status:** Draft
**Scope:** Tokens + lightbox/bottom-sheet chrome scrub. Dark-only.

## Goal

Move the SPA from generic web-app styling toward an industrial,
professional photo-tool feel. Reference: Lightroom Classic and Capture
One — dark-only, dense, low decoration, monospace for numeric metadata,
restrained accent. After this pass, the SPA should feel like a tool
you'd open every day rather than a generic web app.

## Non-goals

- Density toggles (compact/comfortable). Future work.
- Empty-state illustrations or icons. Future work.
- Map page restyling. Already mostly tokenized; minor sweeps only.
- Sidebar / Library / VirtualGrid layout changes. Already industrial.
- Backend changes. The user-settings `theme` endpoint stays even though
  the SPA no longer reads it; other settings will use the same surface.

## Architecture

Two layers:

1. **Token layer** — `frontend/src/app.css`. The canonical palette,
   spacing scale, type stack, radii, and shadow tokens. Inheritance
   carries the bulk of the visual change to components that already use
   the variables.
2. **Chrome scrub** — Replace hardcoded colors, radii, and font sizes
   in the components flagged by the survey (mostly lightbox + bottom
   sheet) with the new tokens.

## Token decisions

### Palette (dark-only)

```css
--bg-base:      #0c0e12   /* page background */
--bg-surface:   #14171c   /* sidebar, sheet, dialog body */
--bg-elevated:  #1a1e25   /* header strip, dropdown, tooltip */
--bg-hover:     #20242c   /* row/button hover */
--bg-overlay:   rgba(0, 0, 0, 0.78)   /* lightbox backdrop, modal scrim */

--text-primary:   #e6e9ef
--text-secondary: #a4abb6
--text-muted:     #6c7480

--accent:       #3a7bd5   /* desaturated blue */
--accent-hover: #4a8bea
--accent-fg:    #ffffff

--border:        #232831  /* default 1px borders */
--border-strong: #2c333d  /* dialog edges, separation lines */

--danger: #d04545
--warn:   #d68a3a
--ok:     #4aaa6a
--orange: #d97706         /* preserved for AI status dot */
```

### Type stack

```css
--font-ui:   "Inter", "SF Pro Text", system-ui, -apple-system, sans-serif;
--font-mono: ui-monospace, "JetBrains Mono", "SF Mono", Menlo, monospace;

--text-xs:   11px
--text-sm:   12px
--text-base: 13px
--text-md:   14px
--text-lg:   16px
```

**Inter**: self-hosted woff2 in `frontend/public/fonts/` (Vite's
default static dir, served at `/fonts/...`). ~50KB for
`Inter-Variable.woff2` covering 100–900 weights. Fallback is
`system-ui` so the SPA renders cleanly even if the font fails to load.

### Spacing scale

```css
--space-1:  2px
--space-2:  4px
--space-3:  6px
--space-4:  8px
--space-5:  12px
--space-6:  16px
--space-7:  24px
--space-8:  32px
```

### Radii

```css
--radius-none: 0
--radius-sm:   2px
--radius-md:   4px
```

The big shift is dropping the existing `--radius: 6px` default. Most
controls go to 2px; modals and dropdowns to 4px. Bottom sheet's 16px
top-rounded corners go to 0 — they're a soft mobile-app tell.

### Shadows

```css
--shadow-none: none;
--shadow-sm:   0 1px 0 rgba(0, 0, 0, 0.4);     /* hairline below header */
--shadow-md:   0 8px 24px rgba(0, 0, 0, 0.45); /* dropdowns, modals */
```

## What gets removed / renamed in `app.css`

Removed:

- `:root.theme-light` block (light palette).
- `@media (prefers-color-scheme: dark)` block (no longer needed; dark
  is the only mode and lives directly on `:root`).
- `color-scheme: light dark` → `color-scheme: dark` so native form
  controls and scrollbars match.

Renamed (the plan task that touches `app.css` also updates every
caller in the same commit so the SPA never lands in a half-renamed
state):

- `--bg-primary` → `--bg-base`. Three call sites today:
  `src/app.css:63` (body), `src/lib/components/ThreeColumnLayout.svelte:34`,
  `src/lib/components/StickyMonthBar.svelte:20` (`color-mix(...)`).
- `--radius` (6px) → `--radius-sm` (2px). Nine call sites across
  components — grep `var\(--radius\)` to enumerate.
- `--shadow` (light/dark variants) → `--shadow-sm`. Two call sites.
- New tokens (`--bg-hover`, `--bg-overlay`, `--accent-hover`,
  `--accent-fg`, `--border-strong`, `--shadow-md`, `--radius-md`,
  spacing scale, font tokens) are additive — no existing call sites
  to update.

## Theme store cleanup

`themeStore` and its dropdown menu lose their reason to exist. Plan:

1. Remove `frontend/src/lib/theme/themeStore.svelte.ts` and
   `themeStore.test.ts`.
2. Remove the `themeStore` instance from `App.svelte` and stop passing
   `theme`/`onSetTheme` to `<AppHeader>`.
3. Remove the theme-section markup and tests from `AppHeader.svelte` /
   `AppHeader.test.ts`. The kebab dropdown shell stays — it gains an
   "About fotobank" entry showing the build version (already exposed
   from `cmd/fotobank/main.go` via ldflags) so the dropdown isn't empty.
4. Backend: leave `/api/v1/settings/user/{key}` untouched. The "theme"
   key just becomes unused; future settings (density, default sort,
   etc.) will use the same endpoint.

## Chrome scrub targets

Files flagged by the visual-foundation survey for hardcoded-value
replacement. Each gets the same treatment: hex/rgba → tokens, hardcoded
radii/sizes → token references.

| File | Hardcoded values to replace |
|---|---|
| `LightboxFrame.svelte` | backdrop `#000`/`rgba(0,0,0,0.92)` → `--bg-overlay`; container bg/radius |
| `LightboxToolbar.svelte` | `rgba(0,0,0,0.5)`, `rgba(255,255,255,0.2)`, 16px font, 4px radii |
| `LightboxNavButtons.svelte` | button bg/border/radius |
| `LightboxMedia.svelte` | image-area bg |
| `LightboxInfoSheet.svelte` | sheet bg, divider, font sizes |
| `LightboxInfoDrawer.svelte` | surface bg, divider |
| `LightboxMetadata.svelte` | font sizes, mono usage for numeric fields |
| `BottomSheet.svelte` | scrim `rgba(0,0,0,0.5)`, sheet bg, 16px top radius → 0 |
| `AppHeader.svelte` | search radius (14px → `--radius-sm`), padding to space tokens |
| `Toast.svelte` | verify token usage; fix any hex literals |
| `ConfirmModal.svelte` | scrim, radius, sizing |
| `AddToAlbumModal.svelte` | same modal shell concerns |
| `ShareModal.svelte` | same modal shell concerns |

This list is the floor, not the ceiling. The plan task for each file
should grep for hex literals (`#[0-9a-f]{3,6}`), `rgba(`, and `px`
font-size literals before claiming the file is done.

## Monospace for metadata

Numeric and technical metadata reads better in monospace because
columns line up. Apply `font-family: var(--font-mono)` to:

- `LightboxMetadata.svelte` — ISO, shutter, aperture, focal length,
  file size, dimensions, lat/lon coordinates.
- AI relevance components (already monospace today; verify they pick
  up the new `--font-mono` token).

The label column (`<dt>`) stays in the UI font; the value column
(`<dd>`) goes mono. Tabular numbers (`font-feature-settings: "tnum"`)
should also be enabled on the mono face for consistent column widths.

## Test strategy

- All 529 existing unit tests must keep passing. They assert structure
  and behavior, not pixels.
- The Playwright e2e suite (~25 tests) must keep passing for the same
  reason. Selectors don't depend on colors.
- No new pure-CSS tests. Visual regressions are caught by manual QA in
  the next phase (SD-card import + browse-the-library spot-check).
- One assertion to add: an `app.css` smoke test that imports the file
  as text and asserts the legacy `--bg-primary` and `theme-light` class
  are gone — guards against accidental partial removal.

## Test cases worth calling out

- AppHeader test: drop the theme-menu tests when the menu is removed;
  add a test for the "About fotobank" item showing a non-empty version
  string.
- Lightbox manual smoke: open a photo, hit ←/→, info drawer opens with
  monospace-aligned metadata, backdrop is opaque-ish charcoal not
  pure-black-with-blur.

## Sequencing

The plan will execute in this order so each commit leaves the SPA in a
working visual state:

1. Replace tokens in `app.css` (palette, type, spacing, radii,
   shadows). At this commit point the SPA already looks distinctly
   different — every component using `var(--*)` inherits the change.
2. Add Inter to `frontend/static/fonts/` and the `@font-face`
   declaration to `app.css`. Wire `--font-ui` to body.
3. Remove `themeStore` and its UI surface; add About item to the kebab
   dropdown.
4. Chrome scrub: lightbox files, bottom sheet, modals.
5. Monospace pass on metadata.
6. Final manual QA across every route.

Each step is a single commit. Subagent-driven execution with two-stage
review between tasks (spec compliance, then code quality).

## Open question

None — direction (B + dark-only + desaturated blue) is locked.

## Acceptance

The pass is done when:

- Every chrome surface listed above uses tokens, not hex/rgba literals.
- `app.css` has no light-mode or `prefers-color-scheme` block.
- `themeStore` is deleted; AppHeader dropdown has a non-empty entry.
- Lightbox metadata reads in monospace with aligned columns.
- All unit + e2e tests still pass.
- Manual QA of /library, /map, /albums/<id>, /search, /hidden, and the
  lightbox confirms no regressions and the Lightroom-like feel lands.
