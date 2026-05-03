# Darkroom Aesthetic Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the "Darkroom Editorial" aesthetic across the SPA, replacing
the prior austere refresh. Canonical reference is
`docs/superpowers/specs/2026-05-03-darkroom-mockup.html` — when in doubt,
open it and match.

**Architecture:** Two layers — token rewrite in `app.css`, then chrome
re-skinning per component. Two new components extracted from
`AppHeader.svelte`: `IdentityChips.svelte` and `SearchBar.svelte`. No
backend changes.

**Tech Stack:** Svelte 5 runes, TypeScript, Vitest, Playwright. Self-hosted
woff2 fonts in `frontend/public/fonts/`.

---

## Task 1: Tokens + self-hosted fonts

**Files:**
- Create: `frontend/public/fonts/IBMPlexSans-Regular.woff2`
- Create: `frontend/public/fonts/IBMPlexSans-Medium.woff2`
- Create: `frontend/public/fonts/IBMPlexSans-SemiBold.woff2`
- Create: `frontend/public/fonts/IBMPlexMono-Regular.woff2`
- Create: `frontend/public/fonts/IBMPlexMono-Medium.woff2`
- Create: `frontend/public/fonts/Fraunces-VariableFont.woff2`
- Modify: `frontend/src/app.css` (full rewrite below the reset)
- Modify: `frontend/src/test/app-css-smoke.test.ts` (extend assertions)
- Modify any caller that uses old token names (search the codebase
  for `--bg-base`, `--bg-surface`, `--bg-elevated`, `--bg-overlay`,
  `--bg-hover`, `--text-primary`, `--text-secondary`, `--text-muted`,
  `--accent` (excluding `--accent-orange` etc. that already exist),
  `--accent-fg`, `--accent-hover`, `--border-strong`, `--radius-sm`,
  `--radius-md`).

- [ ] **Step 1: Download font files into `public/fonts/`.**

Source: Google Fonts repository at https://fonts.google.com/. Pull
woff2 versions (not woff or ttf) for the weights enumerated in the
spec. For Fraunces, pull the variable woff2 (`Fraunces[opsz,SOFT,WONK,wght].woff2`)
and rename to `Fraunces-VariableFont.woff2`. Verify each file exists
and is non-empty before continuing:

```sh
cd /Users/wesm/code/fotobank/frontend
ls -la public/fonts/
```

All six files should be present, each between 30 KB and 200 KB.

- [ ] **Step 2: Rewrite `app.css`** — replace the existing `:root` block
and everything below it (keep the `*` reset at the top) with the full
token surface from the spec, prefixed by `@font-face` declarations:

```css
@font-face {
  font-family: "IBM Plex Sans";
  src: url("/fonts/IBMPlexSans-Regular.woff2") format("woff2");
  font-weight: 400;
  font-style: normal;
  font-display: swap;
}
@font-face {
  font-family: "IBM Plex Sans";
  src: url("/fonts/IBMPlexSans-Medium.woff2") format("woff2");
  font-weight: 500;
  font-style: normal;
  font-display: swap;
}
@font-face {
  font-family: "IBM Plex Sans";
  src: url("/fonts/IBMPlexSans-SemiBold.woff2") format("woff2");
  font-weight: 600;
  font-style: normal;
  font-display: swap;
}
@font-face {
  font-family: "IBM Plex Mono";
  src: url("/fonts/IBMPlexMono-Regular.woff2") format("woff2");
  font-weight: 400;
  font-style: normal;
  font-display: swap;
}
@font-face {
  font-family: "IBM Plex Mono";
  src: url("/fonts/IBMPlexMono-Medium.woff2") format("woff2");
  font-weight: 500;
  font-style: normal;
  font-display: swap;
}
@font-face {
  font-family: "Fraunces";
  src: url("/fonts/Fraunces-VariableFont.woff2") format("woff2-variations");
  font-weight: 100 900;
  font-style: normal;
  font-display: swap;
}

:root {
  /* Palette */
  --bg:        #0a0a0d;
  --surface:   #14141a;
  --surface-2: #1d1d24;
  --border:    #25252c;
  --border-2:  #2e2e36;

  --ink:   #ecebe6;
  --ink-2: #99968d;
  --ink-3: #5a5751;
  --ink-4: #3a3833;

  --amber:      #e8a44b;
  --amber-deep: #c98935;
  --amber-glow: rgba(232, 164, 75, 0.45);

  --ok:     #4aaa6a;
  --danger: #d04545;
  --warn:   #d68a3a;
  --orange: #d97706;  /* preserved for AI status dot */

  /* Type */
  --font-display: "Fraunces", "Iowan Old Style", "Cambria", serif;
  --font-ui:      "IBM Plex Sans", -apple-system, BlinkMacSystemFont, sans-serif;
  --font-mono:    "IBM Plex Mono", "JetBrains Mono", "SF Mono", Menlo, monospace;

  --text-xs:   10.5px;
  --text-sm:   11px;
  --text-base: 12.5px;
  --text-md:   13px;
  --text-lg:   15px;
  --text-xl:   24px;

  --label-track: 0.085em;

  /* Spacing (carry forward) */
  --space-1: 2px; --space-2: 4px; --space-3: 6px; --space-4: 8px;
  --space-5: 12px; --space-6: 16px; --space-7: 24px; --space-8: 32px;

  color-scheme: dark;
}

html, body {
  height: 100%;
  background: var(--bg);
  color: var(--ink);
  font-family: var(--font-ui);
  font-size: var(--text-md);
  line-height: 1.45;
  letter-spacing: -0.005em;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}

/* Film-grain overlay (canonical: see darkroom mockup §body::before) */
body::before {
  content: "";
  position: fixed; inset: 0;
  pointer-events: none;
  background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 220 220'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='0.92' numOctaves='2' stitchTiles='stitch'/><feColorMatrix values='0 0 0 0 0.92  0 0 0 0 0.91  0 0 0 0 0.86  0 0 0 0.18 0'/></filter><rect width='220' height='220' filter='url(%23n)'/></svg>");
  background-size: 220px;
  mix-blend-mode: overlay;
  opacity: 0.6;
  z-index: 100;
}
```

- [ ] **Step 3: Rename callers of dropped tokens.**

```sh
cd /Users/wesm/code/fotobank/frontend
rg "var\(--bg-base\)" -l                       # → var(--bg)
rg "var\(--bg-surface\)" -l                    # → var(--surface)
rg "var\(--bg-elevated\)" -l                   # → var(--surface-2)
rg "var\(--bg-hover\)" -l                      # → var(--surface-2)
rg "var\(--bg-overlay\)" -l                    # → rgba(10,10,13,0.92) inline; or replace whole declaration
rg "var\(--text-primary\)" -l                  # → var(--ink)
rg "var\(--text-secondary\)" -l                # → var(--ink-2)
rg "var\(--text-muted\)" -l                    # → var(--ink-3)
rg "var\(--accent\)" -l                        # → var(--amber)
rg "var\(--accent-fg\)" -l                     # → var(--ink) (or amber-on-amber surfaces)
rg "var\(--accent-hover\)" -l                  # → var(--amber-deep)
rg "var\(--border-strong\)" -l                 # → var(--border-2)
rg "var\(--radius-sm\)|var\(--radius-md\)" -l  # → 0
```

For each match, replace per the comments above. Do this in one commit
so the SPA never lands half-renamed. After replacement, the same greps
should return zero matches.

- [ ] **Step 4: Update `app-css-smoke.test.ts`.**

Keep the existing legacy-removal assertions. Add positive assertions
for the new tokens:

```ts
expect(css).toMatch(/--bg:\s*#0a0a0d/);
expect(css).toMatch(/--ink:\s*#ecebe6/);
expect(css).toMatch(/--amber:\s*#e8a44b/);
expect(css).toMatch(/--font-mono:\s*"IBM Plex Mono"/);
expect(css).toMatch(/@font-face\s*\{[^}]*"IBM Plex Sans"/);
expect(css).toMatch(/body::before/);
```

- [ ] **Step 5: Run typecheck + unit tests + dev server smoke.**

```sh
cd /Users/wesm/code/fotobank/frontend
bun run check
bun run test
```

All tests pass. Then start dev server and confirm the page background
went from cool charcoal to warm near-black, fonts load (DevTools →
Network → filter `fonts/` → six 200 responses, all woff2).

- [ ] **Step 6: Commit.**

```sh
cd /Users/wesm/code/fotobank
git add frontend/public/fonts/ frontend/src/app.css \
        frontend/src/test/app-css-smoke.test.ts \
        frontend/src/lib/components frontend/src/lib/lightbox frontend/src/routes frontend/src/lib/app
git commit -m "feat(frontend/css): darkroom palette + self-hosted Plex/Fraunces fonts"
```

---

## Task 2: AppHeader rewrite (brand + nav + search + identity)

**Files:**
- Create: `frontend/src/lib/components/IdentityChips.svelte`
- Create: `frontend/src/lib/components/IdentityChips.test.ts`
- Create: `frontend/src/lib/components/SearchBar.svelte`
- Create: `frontend/src/lib/components/SearchBar.test.ts`
- Modify: `frontend/src/lib/components/AppHeader.svelte` (rewrite)
- Modify: `frontend/src/lib/components/AppHeader.test.ts` (rewrite)
- Modify: `frontend/src/App.svelte` (drop the "Search" nav entry; pass
  appConfig and search submit handler to AppHeader)

The mockup (`docs/superpowers/specs/2026-05-03-darkroom-mockup.html`)
contains the canonical CSS for `.brand`, `.tabs`, `.search-bar`,
`.id-chip*`. Lift those styles into the new components verbatim.

- [ ] **Step 1: Extract `IdentityChips.svelte`.**

Props: `principal: Principal | null`, `ready: boolean`,
`error?: boolean` (optional; use to flip the dot from `--ok` to
`--danger`).

Renders `null` when `!ready`. Otherwise renders two `<div class="id-chip">`
elements per the mockup. Use `data-testid="id-chip-hub"` and
`data-testid="id-chip-user"` on the wrappers. Style block lifted
from the mockup. Keep the status dot `<span class="id-chip-dot">` only
inside the HUB chip.

- [ ] **Step 2: Write `IdentityChips.test.ts`.**

Three tests:
1. renders nothing when `ready=false`
2. renders both chips when `ready=true` with non-null principal,
   and the value text matches `principal.hub` and `principal.handle`
3. dot color flips when `error=true` (assert via inline style or
   class)

```sh
bun run test src/lib/components/IdentityChips.test.ts
```

All three pass.

- [ ] **Step 3: Extract `SearchBar.svelte`.**

Props: `query?: string`, `onsubmit: (q: string) => void`.

Internal state: a bound `<input type="search">`. ⌘K (Cmd on macOS,
Ctrl elsewhere) focuses the input — wire via a `$effect` that adds a
window keydown listener and calls `inputEl.focus()`. The keydown handler
must `preventDefault()` so the browser's "find" affordance doesn't
steal focus first.

`onsubmit` fires on Enter with the trimmed query (skip the call when
empty).

CSS lifted from the mockup. Use `data-testid="search-input"`.

- [ ] **Step 4: Write `SearchBar.test.ts`.**

Four tests:
1. input renders with placeholder "Search photos, cameras, places…"
2. ⌘K (or Ctrl+K) focuses the input
3. kbd hint becomes invisible (`opacity: 0`) on focus
4. Enter calls `onsubmit` with the trimmed query

- [ ] **Step 5: Rewrite `AppHeader.svelte`.**

Props: `principal: Principal | null`, `ready: boolean`,
`onsearch: (q: string) => void`. Drop existing identity rendering
(currently raw `dev-local: owner` text) and the search-hint button.

Structure:

```svelte
<header class="top">
  <span class="brand">fotobank</span>
  <nav class="tabs">
    <a href="/library" class:active={route === "library"}>Library</a>
    <a href="/map" class:active={route === "map"}>Map</a>
    <a href="/albums" class:active={route === "albums"}>Albums</a>
    <a href="/hidden" class:active={route === "hidden"}>Hidden</a>
  </nav>
  <SearchBar onsubmit={onsearch} />
  <IdentityChips {principal} {ready} />
</header>
```

CSS lifted from the mockup's `.top` / `.brand` / `.tabs` rules. The
existing AIStatusDot is preserved (insert it adjacent to the identity
chips on the right; it stays as a small mounted component).

- [ ] **Step 6: Update `AppHeader.test.ts`.**

Drop the prior identity-text test (`dev-local: owner` no longer renders
as raw text). Keep tests for: brand renders, nav has 4 items (Library,
Map, Albums, Hidden — assert "Search" is NOT in the nav), search input
is reachable, identity chips mount when ready.

- [ ] **Step 7: Update `App.svelte`.**

Wire the search submit handler:

```ts
function onSearchSubmit(q: string) {
  if (!q) return;
  router.push(`/search?q=${encodeURIComponent(q)}`);
}
```

Pass `appConfig.principal`, `appConfig.ready`, `onSearchSubmit` to
`<AppHeader>`. Drop the now-unused `theme` / `onSetTheme` props (already
removed in the prior pass) and the search-hint button.

- [ ] **Step 8: Run typecheck + unit tests.**

```sh
cd /Users/wesm/code/fotobank/frontend
bun run check
bun run test
```

All green.

- [ ] **Step 9: Commit.**

```sh
cd /Users/wesm/code/fotobank
git add frontend/src/lib/components/IdentityChips.svelte \
        frontend/src/lib/components/IdentityChips.test.ts \
        frontend/src/lib/components/SearchBar.svelte \
        frontend/src/lib/components/SearchBar.test.ts \
        frontend/src/lib/components/AppHeader.svelte \
        frontend/src/lib/components/AppHeader.test.ts \
        frontend/src/App.svelte
git commit -m "feat(frontend/header): brand mark + always-visible search + identity badges"
```

---

## Task 3: Sidebar polish

**Files:**
- Modify: `frontend/src/lib/components/Sidebar.svelte`

The existing Sidebar has section heads, list items with counts, an
active state, and a hidden-toggle button. Re-skin to match the mockup's
sidebar block (canonical at `docs/superpowers/specs/2026-05-03-darkroom-mockup.html`
in the `.sb-section` / `.sb-label` / `.sb-list` rules).

- [ ] **Step 1: Re-skin section labels.**

Wrap each section heading in:

```css
.section-label {
  font-size: var(--text-xs);
  font-weight: 600;
  color: var(--ink-3);
  text-transform: uppercase;
  letter-spacing: var(--label-track);
  margin-bottom: 10px;
}
```

- [ ] **Step 2: Re-skin list items.**

Default text color `--ink-2`; hover `--ink`; active `--amber`. No
filled background on active — the amber color alone marks selection.
Counts use `font-family: var(--font-mono)` + `font-variant-numeric:
tabular-nums`, color `--ink-4` (or `--amber-deep` when the row is
active).

- [ ] **Step 3: Hairline divider between sections.**

Drop any existing `border-radius`. Use a 1px `border-top: 1px solid
var(--border)` between sections except the first.

- [ ] **Step 4: Run tests + visual smoke.**

```sh
cd /Users/wesm/code/fotobank/frontend
bun run check && bun run test
```

Open the dev server and walk to /library. Sidebar should match the
mockup's left column.

- [ ] **Step 5: Commit.**

```sh
cd /Users/wesm/code/fotobank
git add frontend/src/lib/components/Sidebar.svelte
git commit -m "refactor(frontend/sidebar): mono counts, hairline rules, amber active"
```

---

## Task 4: Lightbox + modals + toasts re-tokenization

**Files (skin only — behavior unchanged):**
- `frontend/src/lib/components/lightbox/LightboxFrame.svelte`
- `frontend/src/lib/components/lightbox/LightboxToolbar.svelte`
- `frontend/src/lib/components/lightbox/LightboxNavButtons.svelte`
- `frontend/src/lib/components/lightbox/LightboxMedia.svelte`
- `frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte`
- `frontend/src/lib/components/lightbox/LightboxInfoDrawer.svelte`
- `frontend/src/lib/components/lightbox/LightboxMetadata.svelte`
- `frontend/src/lib/components/BottomSheet.svelte`
- `frontend/src/lib/components/ConfirmModal.svelte`
- `frontend/src/lib/components/AddToAlbumModal.svelte`
- `frontend/src/lib/components/ShareModal.svelte`
- `frontend/src/lib/components/ToastStack.svelte`
- `frontend/src/lib/components/ThreeColumnLayout.svelte`
- `frontend/src/lib/components/StickyMonthBar.svelte`

- [ ] **Step 1: Mechanical scrub.** For each file above, run the same
sweep as the previous pass: every `#` hex literal, every `rgba(` /
`rgb(` literal, every `border-radius` declaration, every
`color: white` becomes a token reference per §Chrome treatments in
the spec. The token replacements:

| Was | Becomes |
|---|---|
| any near-white text | `var(--ink)` |
| muted secondary text | `var(--ink-2)` |
| placeholder/label muted | `var(--ink-3)` |
| panel background | `var(--surface)` |
| hover/elevated bg | `var(--surface-2)` |
| page/backdrop | `var(--bg)` |
| backdrop scrim | `rgba(10, 10, 13, 0.92)` |
| any border | `var(--border)` |
| accent (close, primary CTA) | `var(--amber)` |
| accent hover | `var(--amber-deep)` |
| any `border-radius: …` | `0` (delete the declaration) |

- [ ] **Step 2: Convert filled icons to stroked.**

In each lightbox component, search for SVGs using `fill="currentColor"`
without a `stroke=` attribute. Convert to `fill="none"
stroke="currentColor" stroke-width="1.25" stroke-linecap="round"
stroke-linejoin="round"` and adjust the path data only if the icon
becomes illegible at 1.25-stroke (very rare; most heroicons-style
paths render fine).

- [ ] **Step 3: Section labels in `LightboxMetadata.svelte` and
`LightboxInfoDrawer.svelte`.**

Each metadata section gets a `<div class="meta-label">` head per the
mockup's drawer styling. Keep existing `<dl>/<dt>/<dd>` structure.
`<dd>` font-family is already `var(--font-mono)` from the prior pass —
verify and leave alone.

- [ ] **Step 4: Verify.**

```sh
cd frontend
SCRUB="src/lib/components/lightbox \
  src/lib/components/BottomSheet.svelte \
  src/lib/components/ConfirmModal.svelte \
  src/lib/components/AddToAlbumModal.svelte \
  src/lib/components/ShareModal.svelte \
  src/lib/components/ToastStack.svelte \
  src/lib/components/ThreeColumnLayout.svelte \
  src/lib/components/StickyMonthBar.svelte"
(
  cd /Users/wesm/code/fotobank/frontend
  rg "#[0-9a-fA-F]{3,8}\b" $SCRUB
  rg "rgba?\(" $SCRUB | rg -v "rgba\(10,\s*10,\s*13"  # backdrop scrim is allowed
  rg "border-radius" $SCRUB
)
```

All three greps return near-zero matches. Any remaining hits must have
an inline comment justifying them (e.g. `transparent`, an SVG fill
that has to be inline).

- [ ] **Step 5: Run typecheck + unit + e2e.**

```sh
cd /Users/wesm/code/fotobank/frontend
bun run check
bun run test
bun run test:e2e
```

All green.

- [ ] **Step 6: Commit.**

```sh
git add frontend/src/lib/components
git commit -m "refactor(frontend/css): re-tokenize lightbox, modals, toasts to darkroom palette"
```

---

## Task 5: Verification + manual QA log

- [ ] **Step 1: Full automated suite.**

```sh
cd /Users/wesm/code/fotobank/frontend
bun run check
bun run test
bun run test:e2e
bun run test:e2e:sharing-disabled
```

All green.

- [ ] **Step 2: Build + binary smoke.**

```sh
cd /Users/wesm/code/fotobank
make build
bin/fotobank server &
sleep 2
curl -sI http://127.0.0.1:8080/api/v1/healthz
kill %1
```

Server boots, `/api/v1/healthz` returns 200.

- [ ] **Step 3: Manual QA — walk every route.**

Open the binary's web UI in a browser and verify against the mockup:
- `/library`: sidebar matches mockup, photo grid loads with darkroom palette
- `/map`: map page renders, sidebar consistent
- `/albums/<id>` (after creating one): album view in new chrome
- `/search?q=test`: search route still functions; results in new chrome
- `/hidden`: hidden gate UI in new palette
- Lightbox (open any photo): toolbar amber close button, metadata
  drawer with section labels and mono values
- AppHeader: brand mark + 4 nav items + search input + identity chips
  (HUB/USER) all render correctly
- ⌘K: focuses the search input from anywhere in the app
- Identity chips: HUB shows green dot when /api/v1/me succeeded

- [ ] **Step 4: Delete the mockup reference (optional — keep if useful).**

Decision: keep `docs/superpowers/specs/2026-05-03-darkroom-mockup.html`
as historical reference. No deletion.

- [ ] **Step 5: Commit the QA log.**

```sh
cd /Users/wesm/code/fotobank
git commit --allow-empty -m "$(cat <<'EOF'
chore(frontend): darkroom aesthetic — manual QA pass

Walked /library, /map, /albums/<id>, /search, /hidden, and the lightbox
against docs/superpowers/specs/2026-05-03-darkroom-mockup.html. All
surfaces match. ⌘K focuses search; identity chips render with live
status dot. Self-hosted fonts load (DevTools confirmed).

Closes the darkroom aesthetic refresh.
EOF
)"
```
