# Frontend Aesthetic Refresh — "Darkroom Editorial"

**Date:** 2026-05-03
**Status:** Locked (visual direction approved against
[2026-05-03-darkroom-mockup.html](./2026-05-03-darkroom-mockup.html))
**Supersedes:** 2026-05-02-frontend-aesthetic-refresh-design.md (the prior
pass shipped a desaturated, austere palette that read as bland).

## Canonical reference

`docs/superpowers/specs/2026-05-03-darkroom-mockup.html` is the source of
truth for every value in this spec. When the spec and the mockup
disagree, the mockup wins. Open it in a browser to see all decisions in
context: header, sidebar, photo grid, metadata drawer, lightbox toolbar.

## Goal

Move the SPA to a "darkroom editorial" aesthetic: photos are the only
thing with color; chrome is a warm near-black darkroom with a single
amber safelight accent. Distinct typography (no Inter / Roboto / system),
hairline borders, ghost-state controls, mono-everywhere for metadata.
The signature is **`fotobank` set in IBM Plex Mono with an amber dot**.

## Non-goals

- New routes or features. This is purely visual + a few small chrome
  affordances (search bar promotion, identity badges).
- Light theme. Dark only.
- Density toggles. Future work.

## Token decisions

All tokens live in `frontend/src/app.css :root`. The `:root.theme-light`
and `prefers-color-scheme` blocks (already removed in the prior pass)
stay removed.

### Palette

```css
--bg:        #0a0a0d   /* page; warm near-black */
--surface:   #14141a   /* sidebar, drawer */
--surface-2: #1d1d24   /* hover, focused input */
--border:    #25252c   /* hairline */
--border-2:  #2e2e36   /* hover border */

--ink:   #ecebe6       /* primary; warm cream, never pure white */
--ink-2: #99968d       /* secondary */
--ink-3: #5a5751       /* muted */
--ink-4: #3a3833       /* faint */

--amber:      #e8a44b  /* the safelight accent — single accent across the SPA */
--amber-deep: #c98935  /* hover/pressed */
--amber-glow: rgba(232,164,75,0.45)

--ok:     #4aaa6a      /* status dots only */
--danger: #d04545
--warn:   #d68a3a
```

The previous palette's `--bg-base` / `--bg-surface` / `--bg-elevated` /
`--bg-overlay` / `--bg-hover` / `--text-primary` / `--text-secondary` /
`--text-muted` / `--accent` / `--accent-fg` / `--accent-hover` /
`--border-strong` are renamed to the surface/ink scheme above. The
implementation task that touches `app.css` updates every caller in the
same commit.

### Typography

```css
--font-display: "Fraunces", "Iowan Old Style", "Cambria", serif;
--font-ui:      "IBM Plex Sans", -apple-system, BlinkMacSystemFont, sans-serif;
--font-mono:    "IBM Plex Mono", "JetBrains Mono", "SF Mono", Menlo, monospace;
```

All three are self-hosted as woff2 in `frontend/public/fonts/` (no Google
Fonts CDN at runtime). Subset to weights actually used:

- **IBM Plex Sans**: 400 (body), 500 (medium UI labels), 600 (button labels)
- **IBM Plex Mono**: 400 (regular), 500 (brand mark, emphasis)
- **Fraunces** (variable woff2 with `opsz` and `SOFT` axes): one regular file is enough

### Spacing, sizes, radii

Spacing scale carries forward unchanged from the prior pass (2/4/6/8/12/16/24/32 px).

```css
--text-xs:   10.5px    /* uppercase tracked labels */
--text-sm:   11px      /* nav, kbd hint */
--text-base: 12.5px    /* search input, dt/dd values */
--text-md:   13px      /* body */
--text-lg:   15px      /* brand mark */
--text-xl:   24px      /* date stratum headings */

--label-track: 0.085em /* uppercase label letter-spacing */
```

**Radii: zero everywhere.** No `border-radius` on chrome. Sharp corners
read as a tool, not a web app. The prior `--radius-sm`/`--radius-md`
tokens are dropped; any remaining call sites become `0`.

### Texture

Film-grain SVG noise overlay across the full viewport, behind everything
else, at `mix-blend-mode: overlay; opacity: 0.6`. Lives in `body::before`
as an inline SVG data URI (no asset, no request). Keeps the dark from
reading as a flat-LCD dashboard.

## Brand mark

`fotobank` set in **IBM Plex Mono 500 at 15px**, letter-spacing 0,
followed by a 5×5px amber square with a 9px amber glow. No italic, no
serif, no tagline. The glow + mono pairing reads as
control-panel-readout / safelight indicator.

## Identity badges

Two labeled mono chips at the far right of the header. Each chip is a
1px-bordered rectangle with two columns inside, separated by a hairline:

```
┌──────┬────────────┐   ┌───────┬───────┐
│ HUB  │ ● dev-local│   │ USER  │ owner │
└──────┴────────────┘   └───────┴───────┘
```

- Label column: `--ink-3`, 9px Plex Mono 600, uppercase, tracked 0.1em,
  on a slightly darkened bg (`rgba(0,0,0,0.25)`).
- Value column: `--ink`, 11px Plex Mono regular, tabular nums.
- Status dot: 5×5px on the HUB value when /api/v1/me succeeded
  (`--ok` green with a soft glow). Colors red on connection failure
  (we don't have a failure event today, but the dot's color is bound to
  `appConfig.ready && !appConfig.error`).
- Replaces the prior `dev-local: owner` raw text in AppHeader.
- Always rendered (no opt-out for personal use); the chips are clear
  enough about what they are.

## Search bar

The previous "Search ⌘K" hint pill was undersized. Promoted to a real
always-visible `<input type="search">`:

- Sits in the middle column of the header grid (`auto auto 1fr auto`),
  capped at `max-width: 520px`, centered in its slot.
- Height 28px, padding `0 56px 0 32px` (room for icon + kbd).
- Hairline border `--border`; on hover lifts to `--border-2`; on focus
  takes amber border + soft amber outer glow + `--surface-2` background.
- Search icon at left in `--ink-3`; turns `--amber` on focus.
- `⌘K` kbd hint pinned right; `opacity: 0` on focus.
- Placeholder `Search photos, cameras, places, dates…`.
- ⌘K keybind focuses the input (replaces the prior modal-opening
  shortcut). Submitting navigates to `/search?q=…`.

## Nav

Drop **Search** from the nav (replaced by the always-visible search bar).
Remaining items: **Library / Map / Albums / Hidden**. Style:

- 11px Plex Sans 500 uppercase, tracked 0.085em.
- Default color `--ink-3`; hover lifts to `--ink-2`; active is `--ink`
  with a 1px amber underline (with a soft amber glow) flush to the
  bottom of the header. **Never** a filled background.

## Chrome treatments

Across all components:

- Hairline 1px borders in `--border`, `--border-2` on hover.
- 1.25-stroke icons, never filled. Existing `fill="currentColor"` icons
  with no stroke get rewritten to stroked outlines as part of the
  component-level scrub. Use `stroke-linecap="round"` and
  `stroke-linejoin="round"` consistently.
- Ghost-state buttons: transparent bg, `--ink-2` text; hover bg
  `--surface-2`, text `--ink`.
- Active state on tabs/segmented controls: 1px amber underline (with
  soft glow), no filled background.
- Tabular numerics on every numeric `<dd>` and on identity values
  (`font-variant-numeric: tabular-nums`).
- Section labels: `--text-xs` (10.5px) Plex Sans 600, uppercase, tracked
  `--label-track` (0.085em), `--ink-3`.

## Chrome scrub targets

Same set as the prior pass plus AppHeader (which gets a full rewrite).
Each file: replace any remaining hex/rgba literals with tokens, replace
any remaining `border-radius` with 0, replace any remaining
`color: white` with `--ink`.

| File | Treatment |
|---|---|
| `app.css` | full token rewrite + `@font-face` + body grain |
| `AppHeader.svelte` | full rewrite per §Brand/Search/Nav/Identity |
| `Sidebar.svelte` | section labels, mono counts, hairline rules, amber active |
| `lightbox/LightboxFrame.svelte` | backdrop `var(--bg)` at 0.92 alpha; verify no hex |
| `lightbox/LightboxToolbar.svelte` | already opaque from prior pass; re-tokenize colors; close icon amber |
| `lightbox/LightboxNavButtons.svelte` | already square; re-tokenize colors |
| `lightbox/LightboxMedia.svelte` | image area bg → `var(--bg)` |
| `lightbox/LightboxInfoSheet.svelte` | sheet bg, dividers, font sizes |
| `lightbox/LightboxInfoDrawer.svelte` | surface bg, dividers; section labels per §chrome |
| `lightbox/LightboxMetadata.svelte` | already mono from prior pass; verify `.numeric` color, section label styling |
| `BottomSheet.svelte` | scrim, sheet bg, hairline top border, 0px radius |
| `ConfirmModal.svelte` | scrim `var(--bg)/0.78`, hairline border, no radius |
| `AddToAlbumModal.svelte` | same modal shell |
| `ShareModal.svelte` | same modal shell |
| `ToastStack.svelte` | hairline borders, amber for warn, ok green for success |
| `ThreeColumnLayout.svelte` | `var(--bg-primary)` → `var(--bg)`; column borders to `var(--border)` |
| `StickyMonthBar.svelte` | bg + color-mix call site updated to new tokens |
| `AIStatusDot.svelte` | preserve `--orange`, re-tokenize surrounding text |

## Component additions

- **`IdentityChips.svelte`** — encapsulates the HUB/USER badges. Reads
  `appConfig.principal` and `appConfig.ready`. Renders nothing while
  `!ready` (so the header doesn't flash placeholder values during the
  /me round-trip; the rest of the header is still visible).
- **`SearchBar.svelte`** — encapsulates the input + icon + kbd hint.
  Owns the ⌘K keybind. `oninput`/`onsubmit` callback props so the host
  can route to `/search?q=…`.

## Test strategy

- All existing unit tests must keep passing. The previous AppHeader
  tests will need updates: the brand text now reads `fotobank` (without
  italic class), the nav drops "Search", and identity is rendered as
  two chips with `data-testid="id-chip-hub"` / `id-chip-user`.
- New tests:
  - `IdentityChips.test.ts` — renders both chips when ready; renders
    nothing when !ready; falls back to user_id when handle empty (the
    fallback already lives in AppConfigStore so this is mostly a smoke
    test of the rendered DOM).
  - `SearchBar.test.ts` — input is focusable, ⌘K focuses it, kbd hides
    on focus, submit fires the callback with the trimmed query.
- `app-css-smoke.test.ts` — keep the legacy assertions, add new ones for
  the new tokens (`--amber`, `--font-mono`, `--bg`, `--ink`).
- Playwright: existing selectors mostly survive. The search-input
  selector changes from button to input; sharing-disabled e2e fixture
  doesn't touch search.

## Sequencing

Five commits, dispatched as subagent-driven tasks with two-stage review:

1. **Tokens + fonts** — self-host Plex Sans, Plex Mono, Fraunces; rewrite
   `app.css` token surface; update legacy callers in one commit; update
   `app-css-smoke.test.ts`. SPA already looks distinctly different.
2. **AppHeader rewrite** — extract `IdentityChips.svelte` and
   `SearchBar.svelte`; rewrite `AppHeader.svelte`; update tests; wire ⌘K.
3. **Sidebar polish** — section labels, mono counts, hairline rules,
   amber active state.
4. **Lightbox + modals + toasts** — re-tokenize per scrub table; verify
   no hex/rgba/border-radius remains in chrome surfaces.
5. **Verification + manual QA log** — typecheck + unit + e2e; manual QA
   across every route; close-out commit message records findings.

## Acceptance

- `aesthetic-preview.html` and the running SPA agree on every surface.
- No hex/rgba color literals or `border-radius` declarations remain in
  chrome components (lightbox, modals, sidebar, header, toasts, sheets).
- Identity badges render as labeled mono chips, not raw text.
- Search bar is a real input filling the header center, not a hint pill.
- All unit + e2e tests pass.
- Self-hosted fonts load offline (verified via DevTools network panel
  with throttling set to "Offline" after a successful initial load).
