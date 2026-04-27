# Fotobank Web Frontend — Design Spec

> **Status:** Brainstormed and locked 2026-04-26. Pending implementation plan.
>
> **Plan reference:** `~/code/middleman` for SPA build/dev/embed patterns; `~/code/agentsview` for layout and theming inspiration.

## 1. Goal

Ship a self-hosted, AI-first photo-management web UI for fotobank that beats Google Photos at curation, surfaces AI internals honestly, and runs as a single Go binary serving an embedded Svelte SPA. The frontend consumes the existing fotobank backend (Plans A–E plus observability) and adds the surfaces necessary for owner-side photo work: browsing, AI search, albums, sharing, and a privacy-gated Hidden surface.

## 2. Product principles

1. **Better than Google Photos at curation, not feature parity.** The user is coming from Google Photos; the edge is making curation easy. Don't import GP features uncritically.
2. **Lean and performant.** Bundle size, cold-start time, scroll perf, memory footprint, and dependency count are first-class budgets, not afterthoughts.
3. **No GP cruft.** Test for new features: would Google Photos do this? If yes, that's a smell, not an endorsement.
4. **Surface AI internals.** Tags, captions, model identity, and confidence are visible and inspectable. Not hidden behind magic.
5. **Hidden is real privacy.** Server-enforced, threat model documented. Bystander-glance is the threat addressed; encryption-at-rest is a separate v2.

## 3. Architecture overview

- **Single Go binary.** Vite + Svelte 5 SPA built and embedded into the fotobank binary at compile time via `//go:embed dist`. Same listener serves `/api/v1/*` (huma) and `/*` (frontend assets). One artifact, one port, one Make target.
- **Transport: REST + SSE.** SSE for live updates (import progress, AI tagging completion, share status flips, AI health). No WebSocket in v1.
- **AI gateway.** `internal/ai/gateway.ModelGateway` interface with one `OpenAICompatible` HTTP impl. Two configurable endpoints (`embeddings_url`, `vision_url`); tokens stay server-side and never reach the browser.
- **Forward-compat for viewer-only accounts.** v1 is owner-only, but identity middleware, scope models, and routing don't assume "always owner." Adding a viewer-only context later is additive, not refactor.

## 4. Layout & navigation

Three-column shell modeled on agentsview.

```
┌──────────────────────────────────────────────────────────────┐
│ shell strip:  [logo] [identity]      [🔍 search ⌘K]  [👤]    │
├─────────┬──────────────────────────────────────────┬─────────┤
│ Sidebar │ Main pane                                │ Detail  │
│         │                                          │ rail    │
│ BROWSE  │                                          │ (collap-│
│   Library│                                         │  sible) │
│   Sessions│                                        │         │
│   Map   │                                          │         │
│ CURATE  │                                          │         │
│   Albums│                                          │         │
│ PRIVATE │                                          │         │
│   Hidden│                                          │         │
│ ─────── │                                          │         │
│ Settings│                                          │         │
└─────────┴──────────────────────────────────────────┴─────────┘
```

**Sidebar contents.**

- **BROWSE:** `Library` (default landing, opens at latest capture date), `Sessions` (4-hour-gap clustering), `Map` (geotagged photos).
- **CURATE:** `Albums` — top-5 recent/pinned children + "All albums…" leaf + "+ New album." Pin count configurable in Settings (3–10).
- **PRIVATE:** `Hidden` — only rendered when an `auth_hidden_passcode` row exists; click triggers re-auth.
- **Settings** (bottom-pinned): inference servers status, map config status, geocoder status, theme, density prefs, AI worker stats, build info.

**Shell strip.** Always-visible search input on the right, bound to `⌘K`. Typing replaces the main pane with `/search?q=...&filters=...`. Closing search returns to the previous browse context with scroll position preserved. Identity indicator is left of search; v1 shows a single principal but the slot is reserved for viewer-only contexts later.

**Default landing.** Library at the latest capture date (not literally "today" — if you import 2018 photos this morning, the grid lands where the photos are). Last-viewed-position restore is a v2 polish.

**Mobile.** Sidebar collapses to a top-left drawer; detail rail becomes a bottom sheet or full-screen details view, never the 280 px side panel. Mobile is utilitarian only; curation is desktop-first.

## 5. Library, Sessions, Map

### 5.1 Library — date-grouped grid

- **Layout: justified rows** (Lightroom/Flickr style). Photos keep aspect ratio; equal row heights, variable cell widths. Square cells and Pinterest masonry rejected.
- **Headers:**
  - Sticky month bar at top of viewport during scroll. Quiet typography, translucent background, no buttons. Pure orientation.
  - Inline day headers above each day's photos: `Sat Apr 18 · 23 photos · Hayes Valley`. Location is best-effort: dominant cluster (≥70 % of geotagged photos within ~1 km radius) → place name; else `3 locations`; else omit. No false precision.
  - Right-edge year scrubber (Apple Photos style) for fast jump.
- **Selection model (always-selectable; no separate "select mode"):**
  - Click → open lightbox.
  - ⌘-click → toggle in selection.
  - Shift-click → range from last selected.
  - Esc clears.
  - Long-press on mobile → enter selection / toggle item.
  - When ≥1 selected, action bar slides into the shell strip: `3 selected · Add to album · Hide · Share · Done`. **No Delete in v1** — destructive deletion ships only with a real recovery story.
- **Density: target row height** (Compact / Comfortable / Large) plus `+`/`-` keyboard shortcuts. Internal: `rowTargetHeight, minRowHeight, maxRowHeight`. Persists per browse context. Columns are emergent.
- **Cell content:** thumbnail only. Top-right corner badges where applicable (video play, share-out, eye-strike-only-in-Hidden-view). No hover overlays — the photo is the content.
- **Performance approach:**
  - Custom virtualization, chunked by month. Each month chunk computes its own justified-row layout once on first visibility, caches it, unmounts when off-screen.
  - `content-visibility: auto` on every cell, **paired with stable intrinsic sizing per chunk and per cell** from the layout cache so skipped content does not cause scroll jumps.
  - `loading="lazy"` and `decoding="async"` on every `<img>`.
  - Thumbnails stay JPEG (current pipeline; pure-Go AVIF/WebP encoders aren't production-grade and would fight the no-CGO rule). Honest perf framing: the win is from keeping decode off the main thread and skipping paint of off-screen cells, not from a magic codec.
  - No BlurHash in v1; gray placeholders on stable aspect-ratio boxes. Revisit only after measurement.

### 5.2 Sessions — time-clustered view

- Photos cluster when consecutive shots have a time gap < `session_gap` (default 4 h, configurable).
- **Computed at query time, not persisted.** No DB rows for sessions. The view groups results from `media` ordered by `taken_at` and chunks at gap boundaries.
- Title format mirrors Library day headers: `Sat Apr 18 · 23 photos · Hayes Valley`.
- Same grid mechanics, same selection / lightbox semantics as Library.
- Hidden excluded by default (same rule).

### 5.3 Map — geotagged-only

- **MapLibre GL JS** (WebGL, GPU-accelerated clustering). Bundle weight (~150 KB gzipped, lazy-loaded only when Map opens) accepted because Leaflet's DOM markers don't scale at 100 K-photo scale.
- **Tile source: user-configured, no defaults shipped.** Until configured, Map sidebar entry shows a setup card pointing to Settings → Map.
- **Tile proxy at `/api/v1/map/tiles/{z}/{x}/{y}`.** Frontend uses this URL exclusively. Backend signs upstream URL with the configured API key (server-side only). HTTP cache headers honored end-to-end. Hardening:
  - `z ∈ [0, 22]`, `x ∈ [0, 2^z)`, `y ∈ [0, 2^z)` — out-of-range rejects 400.
  - Substitution restricted to `{z}/{x}/{y}` plus the pre-configured key placeholder. No path traversal.
  - Refuses any request that would yield a non-template URL. Not a general fetcher.
- **Photo data: server-side clustering with point fallback.**
  - `GET /api/v1/media/geo?bbox=&from=&to=&zoom=&cap=10000` returns GeoJSON.
  - Server-side clustering kicks in when `zoom < threshold` OR raw point count > `cap`. `cap` default 10 000; server-bounded upper limit 25 000 regardless of caller.
  - Below threshold and below cap: singletons. Above: clusters with `point_count`. Cluster click zooms in (re-query at new bbox/zoom).
  - `WHERE hidden_at IS NULL` always — Hidden never affects cluster counts or singletons.
- **Map UX:**
  - Full-pane MapLibre with toolbar: date range slider, fit-to-all, zoom controls.
  - Singletons render as pins; clusters as bubbles with count.
  - Singleton click → info panel (right rail). Singleton double-click → lightbox.
  - **Area-select tool** (explicit toolbar button) toggles draw-rectangle mode → `/search?filters=near:bbox=...`. Doesn't fight pan/box-zoom.
  - **Attribution rendered on the map** via MapLibre's standard attribution control, populated from tile-source settings.

### 5.4 Reverse geocoding

- Backend queued worker fills `media.location_label`. Configured Nominatim-API-shape endpoint in Settings → Map → Geocoder. Public Nominatim never hit by default (per Nominatim usage policy on bulk).
- Cached per coord; "Refresh location" button in the lightbox info panel re-queues.
- Until configured, info panels show coords as text only.

### 5.5 GPS backend slice (new)

The current `media` table has no GPS columns and `internal/exifread/exifread.go` does not extract GPS. This is real backend work, not a thin layer:

- Schema additions to `media` (folded into `000001_initial_schema.up.sql` per pre-prod policy): `latitude REAL NULL`, `longitude REAL NULL`, `gps_at TIMESTAMP NULL`, `location_label TEXT NULL`.
- `internal/exifread/exifread.go`: parse `GPSLatitude`, `GPSLongitude`, refs, `GPSDateTime`; convert DMS → decimal.
- Importer persists GPS during ingest.
- Media repo + API expose GPS fields (raw lat/lon owner-side; `location_label` only on grantee-side).
- Reconciler / backfill task re-extracts EXIF for existing photos with `gps_at IS NULL`.

## 6. Lightbox

```
┌──────────────────────────────────────┬─────────────┐
│  234 / 1,847  ☑ in selection (3)     │           ✕ │
│                                      │  CAPTURE    │
│                                      │  Sat Apr 18 │
│         [photo, fitted to            │  Sony A7R V │
│          preview-tier (2560)]        │  f/2.8 1/250│
│                                      │  LOCATION   │
│                                      │  Hayes Valley│
│                                      │  [mini-map] │
│                                      │  AI TAGS    │
│       [⊕] [⊘] [↗] [ⓘ] [1:1]          │  CAPTION    │
└──────────────────────────────────────┴─────────────┘
```

**Default image source — fitted derivative.** Lightbox loads the `preview` tier (~2560 px long edge) by default.

**Source ladder:**

1. `preview` derivative (2560 px) — always.
2. Original — only when zoom > 1.5× OR `1` pressed (1:1) AND the original is browser-decodable (JPEG, modern HEIC where supported, common video codecs).
3. Otherwise — "Download original" link/action; no inline display. RAW (`.ARW`, `.NEF`, `.CR2`, etc.) always falls into (3) for inline display, but `1` shows 1:1 of the **`large` tier** (~4096 px), never sensor-pixel resolution.

**Thumb pipeline updates** (`internal/thumb/sizes.go`):

- v1 sizes: `grid=256`, **`preview=2560`** (replaces the prior `lightbox=2048` constant), **`large=4096`** (new tier).
- For RAW source media, `large` tier extracts the embedded camera JPEG; no rawconvert needed.
- Backfill: regen all media on first import after upgrade. The plan writes this as an explicit task.

**Zoom / pan inputs.**

- **Trackpad:** pinch = zoom toward gesture center; two-finger drag (zoomed) = pan; two-finger swipe (fit) = next/prev. Trackpad two-finger gesture detection ensures swipe never zooms.
- **Mouse wheel:** wheel = zoom toward cursor (Lightroom convention). Hold `Space` + drag = pan when zoomed.
- **Keyboard:** `←/→` next/prev · `↑/↓` next/prev day · `+/-` zoom · `0` fit · `1` 1:1 · `i` info · `Esc` close · `h` toggle hide · `s` share · `a` add to album · `m` toggle mark/select · `f` fullscreen · `Space` no-op on stills (pan modifier with drag) / play-pause on video.
- **Touch:** tap = toggle chrome · double-tap = fit↔2× · pinch · drag (zoomed) = pan · swipe (fit) = next/prev.

**Info panel.**

- Default off; opened with `i`; persists across photos within the lightbox session, resets on close.
- Slides in from the right; image area shrinks by 280 px. **CSS fit recomputes on toggle (cheap reflow); no source swap or redecode.**
- Mobile: bottom sheet or full-screen details view.
- Sections: Capture (date, camera, exposure), Location (label + mini-map only when tile source configured), AI Tags (clickable chips), Caption (text + model identity + generation timestamp), File (filename, size, dimensions), Status (Visible/Hidden, share status).
- Tag click → `/search?filters=tag:dog` (lightbox closes; search opens with the tag chip pre-applied).

**Map mini-map privacy.** Renders only when user has configured a tile source in Settings → Map. Without configuration, location section shows label/coords text only — no silent third-party tile requests.

**Selection bridge.**

- Grid selection preserved when entering and exiting the lightbox.
- By default, lightbox navigates the **full** current browse context (Library, Album, Search results — whatever you came from).
- Click "in selection (N)" indicator to toggle "navigate selection only."
- `m` toggles the current photo's grid selection.

**Action cluster** (bottom-center, auto-fade after 2 s of inactivity):

- `Add to album (a)` · `Hide (h)` · `Share (s)` · `Info (i)` · `1:1 (1)`.
- `h` flips `hidden_at`. Toast: `Hidden · Undo` (5 s window). Photo immediately leaves the current sequence; lightbox advances.
- `s` opens the share dialog. If the photo is hidden, dialog forces `[Unhide and share] [Cancel]` — no share-as-hidden path in v1.

**Video.**

- HTML5 `<video preload="metadata">`. Originals streamed via `/original`. Range support already present in `internal/httpapi/originals.go`; the spec gap is end-to-end Playwright verification, not adding Range.
- Autoplay off; `Space` = play/pause; `j`/`l` = ±10 s; `m` = mark/select (consistent with stills); video mute lives on the speaker button + `Shift+M`. `f` = fullscreen.
- v1 streams originals only. Transcoded preview tier for HEVC/uncommon codecs is a v2 perf optimization once measurements call for it.

## 7. Search

**Where it lives.** Always-visible input in the shell strip, right-aligned, bound to `⌘K`. Typing replaces the main pane with `/search?q=...&filters=...`. Closing returns to the previous context with scroll position preserved.

**Inputs — pure semantic + explicit chips.**

- Text input: free text only.
- "Filters" button next to input opens a popover with date range, tag picker (autocompleting against active tag vocabulary), location input, **media type** (photo/video — cheap predicate, included).
- Each filter becomes an editable chip below the input.
- **No prefix syntax** in v1 (`tag:`, `before:`); revisit if power users ask.

**Scope.** v1 search is **global**. Context-scoped search ("within this album") = v2.

**Sort.** Segment control: `Relevance | Newest | Oldest`. Defaults to Relevance when text is non-empty, Newest otherwise.

**Backend — hybrid (semantic + lexical).**

- `POST /api/v1/search { query?, filters[], sort, limit, cursor }` → `{ results, next_cursor, has_more, embedding_completeness }`.
- Hybrid scoring: semantic embedding similarity ∪ exact/lexical matches against `filename`, `caption_text`, `tag_label`, `camera`, `lens`, `location_label`. Lexical via SQLite **FTS5** virtual table `media_fts`.
- Fusion strategy: **reciprocal rank fusion (RRF)** as the v1 default, with constant `k = 60`. Spec writes the rule; perf tuning post-launch.
- Filter-first SQL when filter selectivity is high; top-K vector candidates then filter when low. Engine picks per estimated selectivity.
- Exact `total` only when filter-only (cheap COUNT). Relevance pages use cursor pagination.

**Hidden interaction.** Global search applies `WHERE hidden_at IS NULL`. Only inside the unlocked Hidden context with explicit `Include hidden` toggle does Hidden become searchable. Without unlock, `include_hidden=true` returns **403 generic**, no body content distinguishing "exists" vs "doesn't exist."

**Indexing status, two-tier:**

- Compact pill in the search header normally: `2,143 / 2,981 indexed`.
- Elevates to a banner only when (a) query has non-empty text AND (b) embedding completeness < 80 %.

**Score indicator → Diagnostics mode.** Tucked under Settings → AI Inspection toggle. Power users opt in; normals never see it.

**Vector index — sqlite-vec candidate, behind a prototype gate.** Pre-v1 upstream; pure C; `modernc.org/sqlite/vec` is a likely path but needs a spike. Spec retains a `SearchIndex` interface and a brute-force fallback (fine to ~100 K vectors). Decision deferred until the spike lands.

## 8. AI surfaces

### 8.1 Where AI shows up

- **Grid cells:** no AI chrome. Stays clean.
- **Lightbox info panel:** tags as chips, caption + model identity + generation timestamp.
- **Search results:** same grid mechanics; cells don't render tags/captions; ranking respects hybrid scoring.
- **Tag filter chips** (search popover): autocomplete against `media_tags` rows where `status = 'active'`.
- **Shell strip AI indicator:** small status dot near Settings — **color + icon + `aria-label`**, never color-alone. States: `idle/healthy`, `backlog (>N pending)`, `failing (>X failures/hour)`, `unreachable (server unconfigured or down)`. Click → Settings → AI panel. Reads from `GET /api/v1/ai/health`.

### 8.2 Tag click flow

Click any tag chip in the lightbox info panel → navigate to `/search?filters=tag:dog`. Single flow, used everywhere tags appear.

### 8.3 Tag taxonomy: open vocabulary

- VLM emits free-text tags per photo at processing time.
- **Storage split:** `tag_key` (normalized: lowercase, punctuation stripped, whitespace collapsed) for matching, `tag_label` (original model output) for display + provenance.
- **Promotion rule:** top-K candidates with score floor — defaults `K=10`, `score ≥ 0.4`, both gates required. Promoted → `active`. Rest → `candidate`.
- **Autocomplete vocabulary** = `tag_key` where `status = 'active'`, excluding candidates.
- Filter chips are exact-match by design (explicit recall). Synonyms handled implicitly by free-text semantic ranking.

### 8.4 Captions: read-only in v1

- Display in lightbox info panel only. Not a primary ranking signal in search; included in lexical match (FTS5 over `caption_text`).
- User-editable + regen-able = v2 (requires `ai_caption` + `user_caption` split, UI affordance).

### 8.5 AI artifact storage rule (universal)

Every artifact carries `(media_id, task_type, model_id, prompt_version, generated_at, status)` where `task_type ∈ {embed, tag, caption}` and `status ∈ {active, candidate, stale}`.

**Active model registry:** `ai_active_models(task_type PK, model_id, prompt_version, since)`. Single row per task; rotation is a row update.

**Replacement semantics on model swap:**

- Old `active` artifacts STAY active while a new run is pending or fails per row.
- **On success per row:** atomic transaction marks old artifacts `stale` AND promotes new artifacts to `active`.
- **On failure per row:** old `active` retained. The `(media_id, task_type)` pair is recorded as `reindex_failed` and surfaced in Settings → AI as `127 photos failed under llava-1.6 · view & retry`.

**Embeddings cannot mix model spaces.** Semantic search joins on `model_id = active_embedding_model`. Rows under prior models count as unindexed for semantic; lexical search still matches them.

### 8.6 AI panel (Settings → AI)

- **Servers:** embeddings URL, vision URL, model name per task, last health-check status, test-connection (server-side; SPA receives verdict only).
- **Per-worker stats:** queue depth, throughput (photos/min), last completion, failure count last hour. Backed by `obs.Metrics` so the same numbers feed Prometheus.
- **Reindex actions:** `Re-embed all`, `Re-caption all`, `Re-tag all`. Each kicks the corresponding worker against the full library; per-task progress shown.
- **Failed jobs list:** `(media_id, error, model_id, prompt_version, attempted_at)`; retry-all button.
- **Diagnostics toggle:** unlocks per-cell score indicator in search results.
- **Credentials redaction:** SPA never sees tokens or API keys. Settings panel shows host + model name + status only.

### 8.7 Out-of-scope for v1 (named non-goals)

- Face detection / "People" tab.
- Smart album suggestions / "Memories" / "For You".
- AI-driven image editing (auto-enhance, magic eraser).
- Multi-modal prompts (drop-photo + text query).

### 8.8 Open spikes

- Vector index: `sqlite-vec` extension via `modernc.org/sqlite/vec` vs BLOB+brute-force.
- Tag confidence calibration: are VLM tag scores comparable across models? If not, promotion rule is rank-based only (top-K), not score-floored.

## 9. Hidden / privacy gate

### 9.1 Threat model (explicit)

Hidden is **app-level privacy, not encryption.** The threat addressed is "someone glances at my browser while I'm scrolling photos" — bystander glance, not device compromise. Server admins, DB access, NAS access, and CLI admin retain full visibility and reset capability.

### 9.2 Server-enforced

Client-only hiding is fake security — devtools bypass it. Backend enforces:

- Default `WHERE hidden_at IS NULL` on every photo-returning endpoint (Library, Sessions, Map, Search, Albums, shared scopes, **byte endpoints**).
- `include_hidden=true` accepted only when the request carries the unlock cookie.
- Without unlock, `include_hidden=true` returns **403 generic**. No body content distinguishes "hidden photos exist" from "no hidden photos exist."

### 9.3 Re-auth flow

1. User clicks `Hidden` sidebar item.
2. Modal: "Enter passcode."
3. `POST /api/v1/auth/hidden/challenge { passcode }` → server verifies argon2id hash → on match issues opaque 32-byte random token in `__Host-fotobank-hidden` cookie.
4. Hidden context loads. Shell strip shows `Hidden unlocked · expires 4:23 · [Lock]`.
5. On TTL expiry, manual Lock, route change away from Hidden, or `visibilitychange` to hidden tab → `POST /api/v1/auth/hidden/lock` revokes session and clears cookie.

### 9.4 Cookie attributes

- **Name:** `__Host-fotobank-hidden` (prod, HTTPS) / `fotobank-hidden` (dev, HTTP — explicit fallback in code).
- **Value:** opaque 32-byte random base64url token. **Zero privilege data.**
- `HttpOnly`, `Secure` (prod), `SameSite=Strict`, `Path=/` (required by `__Host-` prefix), `Max-Age=300`.

### 9.5 Session storage

`auth_hidden_session(token_hash PRIMARY KEY, principal_hub, principal_user_id, issued_at, expires_at, revoked_at NULL)`.

- Lookup by `token_hash`; cookie value is hashed before storage.
- Valid iff `revoked_at IS NULL AND expires_at > now()`.
- Cleanup index: `auth_hidden_session(expires_at) WHERE revoked_at IS NULL` for cheap purge.

### 9.6 Failed-attempt backoff

5 failures in 1 minute → 5-minute lockout per principal (server-tracked).

### 9.7 Setup / change / disable

- **Setup gate.** Hidden sidebar item is absent and Hide actions are absent until an `auth_hidden_passcode` row exists. Setup flow lives in Settings → Privacy.
- **Change passcode.** Requires current passcode; revokes all active sessions for that principal.
- **Disable Hidden — atomic.** Confirmation copy: `Unhide 423 photos and disable Hidden?` Single transaction: clears all `hidden_at` for the owner, deletes `auth_hidden_passcode` row, revokes all active sessions, emits an audit log entry.

### 9.8 Hide / unhide actions

- **Hide** (anywhere — grid action bar, lightbox `h`, etc.) sets `hidden_at = now()`. No re-auth required: you're hiding *your own* photo. Asymmetric by design — the rate of "hide accidentally" is much higher than "unhide accidentally."
- **Unhide** (only inside the unlocked Hidden context) clears `hidden_at`. Requires the unlock cookie.

### 9.9 Recovery

No recovery in v1. Lost passcode = `fotobank admin reset-hidden-passcode --confirm` on the server (CLI only). Doesn't touch `hidden_at` values; lets you set a new passcode. NAS-stored recovery key file = v2.

### 9.10 Argon2id parameters (OWASP-class)

- `m=19456 KiB`, `t=2`, `p=1`.
- Stored: `algo='argon2id', params={m,t,p}, salt, hash`.
- `golang.org/x/crypto/argon2`.

### 9.11 API

- `POST /api/v1/auth/hidden/setup { passcode }` (first-time only)
- `POST /api/v1/auth/hidden/change { current, new }` (revokes active sessions)
- `POST /api/v1/auth/hidden/disable { passcode }` (atomic unhide-then-disable)
- `POST /api/v1/auth/hidden/challenge { passcode }`
- `POST /api/v1/auth/hidden/lock`
- `GET /api/v1/auth/hidden/status` → `{ configured, unlocked, expires_at? }`

## 10. Albums

Plan D backend covers the **core manual album substrate**: CRUD, add/remove, list media, derived cover, owner-consistency triggers, `album_media.position` column. **This section adds web-facing album metadata, content ordering, per-user UI prefs, and hidden-aware payloads.**

### 10.1 Schema additions (folded into 000001)

- `albums.description TEXT NULL`.
- `albums.cover_media_id UUID NULL REFERENCES media(id) ON DELETE SET NULL`. Owner-consistency enforcement: trigger mirroring the existing `album_media_owner_consistency_insert` pattern, plus service-level check in `service.AlbumService.SetCover` as defense-in-depth.
- `user_album_prefs(principal_hub, principal_user_id, album_id, pinned_at TIMESTAMP NULL, sort_pref TEXT NULL, PK(principal_hub, principal_user_id, album_id))`.

### 10.2 API additions

- `PATCH /api/v1/albums/{id}` accepts `description`, `cover_media_id`.
- `PATCH /api/v1/albums/{id}/media/{media_id} { position }` for single-photo reorder.
- `POST /api/v1/albums/{id}/media:reorder { positions: [{media_id, position}] }` for drag-end batch update.
- `POST|DELETE /api/v1/albums/{id}/pin`.
- List/detail responses gain `{ visible_count, hidden_count, shared, share_count }`.

### 10.3 v1 album UX

- **Manual albums only.** Smart/rule-based albums = v2.
- **Index page (`/albums`):** card grid, search-by-name, sort `Recently modified | Alphabetical`. **No custom-order index in v1.**
- **Detail page (`/albums/{id}`):** editable-inline title, editable-inline description, `124 photos · 7 hidden` honest count, date range, owner-picked cover (or derived 4-photo mosaic), justified grid.
- **Reorder within album** via drag → `album_media.position`. Sort options: `By position (default) | By date taken | By date added`.
- **Add** via picker from grid action bar (`Add to album`) or lightbox (`a`). Multi-album-add allowed in one action. Drag-onto-sidebar deferred to v2.
- **Remove** only from album detail; toast with 5 s undo. Doesn't delete photos — just `album_media` rows.

### 10.4 Hidden honesty

- Album header chip and count always shown when `hidden_count > 0`, regardless of unlock state.
  - Locked: `124 photos · 7 hidden · Unlock to view`.
  - Unlocked: `124 photos · 7 hidden`; grid includes them with eye-strike badge.
- **Hidden in shared albums:** grantee reads always exclude hidden members. Share dialog AND share management view show explicit copy: `7 hidden photos in this album are not shared.`

### 10.5 Delete album with live shares (two-phase)

Cannot atomically revoke remote grants and delete local rows.

- Modal: `2 active shares · [Revoke shares] [Cancel]`.
- After revocation succeeds (broker round-trip), modal flips to `Revocations complete · [Delete album]`.
- One-click flow available: queues revocation, polls, then continues — but it is not one DB transaction.

## 11. Sharing

Plan E1 (scopes) + Plan E2 (broker outbox worker) provide the backend. v1 web UI adds the share dialog and management surfaces.

### 11.1 Scope model

- Album share → `target_type=album_live`.
- One or more selected photos → `target_type=media_set` (frozen `scope_media` membership, 1..1000 media IDs). One-photo share is just `media_set` with N=1.
- **No `target_type=media`** in current model.
- **Multiple recipients = one scope per recipient.** Each scope can be a media_set carrying all selected photos.

### 11.2 Recipient input

- **Canonical** = `(grantee_hub, grantee_user_id)`. Stored structured.
- **Display + autocomplete:** `@handle` from previously-used grantees.
- v1 web UI does not resolve handles — broker is the resolver (forward-compat for external service).

### 11.3 Permissions

Read-only in v1 (no UI toggle).

### 11.4 Optional expiry

- `scopes.expires_at` column required; verification listed in §17.
- v1 expiry **stops serving** (existing E2 read enforcement); scopes remain visible as `Expired`; owner manually revokes.
- **Auto-revoke sweep is a v2 candidate, not v1.**

### 11.5 Hidden interaction

If any selected item is hidden: dialog forces `[Unhide N and share] [Cancel]`. No share-as-hidden in v1.

### 11.6 Where sharing is triggered

- Album header `Share…` (kebab menu).
- Selection action bar `Share…`.
- Lightbox `s`.

### 11.7 Share dialog UI

- Recipient input (autocomplete + free-form `hub:user_id`).
- Optional expiry date picker (default none).
- Hidden warning if any selected items are hidden.
- `Share` button creates scope(s); status → `pending`; outbox publishes asynchronously.

### 11.8 Share management (Settings → Shares)

Promote to top-level only if it grows enough to deserve nav.

- List of scopes with status badge mirroring `BrokerStatus`: `Pending · Publishing · Live · Revoking · Revoked · Expired · Failed`. Color + icon + tooltip per a11y rule.
- Per-row actions: `Open` (jump to target), `Revoke`, `Retry` (when `Failed` and `attempts < max`).
- Filter by status (default hides `Revoked`).
- Endpoint: `GET /api/v1/shares?status=...&cursor=...` (verify it exists; add if missing).

### 11.9 Share badges (v1 surfaces)

- Album cards / album header: chip from existing scope query projection.
- Lightbox info panel: `Status: Visible · Shared with @bob @alice` from per-photo scope query.
- **Photo cell badges deferred** until a `media.shared_count` projection exists. `album_live` expansion + `media_set` lookup is too expensive without a projection. v2 candidate (listed in §15).

## 12. Backend chapter — schema, packages, APIs, observability

### 12.1 Schema policy: pre-prod squash

**Until first prod deployment, all schema changes for the web frontend are folded into `internal/db/migrations/000001_initial_schema.up.sql` and the matching `.down.sql`.** This is **"Initial schema edits while pre-prod."** Once fotobank has real deployed databases, this policy ends and all future schema changes use sequential migrations with up/down pairs.

**Freeze point:** the first prod deployment. Determined externally (operator deployment event); spec writer / plan writer treats this section's policy as conditional on `pre_prod==true`.

### 12.2 Schema deltas (folded into 000001)

```sql
-- media additions
ALTER TABLE media
  ADD COLUMN hidden_at        TIMESTAMP NULL,
  ADD COLUMN latitude         REAL NULL,
  ADD COLUMN longitude        REAL NULL,
  ADD COLUMN gps_at           TIMESTAMP NULL,
  ADD COLUMN location_label   TEXT NULL;

CREATE INDEX media_visible_owner_taken_idx
  ON media(owner_hub, owner_user_id, taken_at)
  WHERE hidden_at IS NULL;

CREATE INDEX media_visible_geo_idx
  ON media(owner_hub, owner_user_id, latitude, longitude)
  WHERE hidden_at IS NULL AND latitude IS NOT NULL;

-- albums additions
ALTER TABLE albums
  ADD COLUMN description       TEXT NULL,
  ADD COLUMN cover_media_id    UUID NULL REFERENCES media(id) ON DELETE SET NULL;

CREATE TRIGGER albums_cover_owner_consistency_update
BEFORE UPDATE OF cover_media_id ON albums
FOR EACH ROW
WHEN NEW.cover_media_id IS NOT NULL
BEGIN
  SELECT CASE
    WHEN (SELECT owner_hub FROM media WHERE id = NEW.cover_media_id) != NEW.owner_hub
      OR (SELECT owner_user_id FROM media WHERE id = NEW.cover_media_id) != NEW.owner_user_id
    THEN RAISE(ABORT, 'cover_media_id owner mismatch')
  END;
END;

-- per-user album prefs
CREATE TABLE user_album_prefs (
    principal_hub        TEXT NOT NULL,
    principal_user_id    TEXT NOT NULL,
    album_id             UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    pinned_at            TIMESTAMP NULL,
    sort_pref            TEXT NULL,
    PRIMARY KEY (principal_hub, principal_user_id, album_id)
);

-- hidden auth
CREATE TABLE auth_hidden_passcode (
    principal_hub        TEXT NOT NULL,
    principal_user_id    TEXT NOT NULL,
    algo                 TEXT NOT NULL,
    params               TEXT NOT NULL,  -- json: {m,t,p}
    salt                 BLOB NOT NULL,
    hash                 BLOB NOT NULL,
    created_at           TIMESTAMP NOT NULL,
    updated_at           TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id)
);

CREATE TABLE auth_hidden_session (
    token_hash           BLOB PRIMARY KEY,
    principal_hub        TEXT NOT NULL,
    principal_user_id    TEXT NOT NULL,
    issued_at            TIMESTAMP NOT NULL,
    expires_at           TIMESTAMP NOT NULL,
    revoked_at           TIMESTAMP NULL
);

CREATE INDEX auth_hidden_session_expiry_idx
  ON auth_hidden_session(expires_at)
  WHERE revoked_at IS NULL;

-- AI artifacts
CREATE TABLE media_tags (
    media_id          UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    tag_key           TEXT NOT NULL,
    tag_label         TEXT NOT NULL,
    score             REAL NOT NULL,
    model_id          TEXT NOT NULL,
    prompt_version    TEXT NOT NULL,
    generated_at      TIMESTAMP NOT NULL,
    status            TEXT NOT NULL,  -- active|candidate|stale
    PRIMARY KEY (media_id, tag_key, model_id, prompt_version)
);

CREATE INDEX media_tags_active_idx
  ON media_tags(tag_key)
  WHERE status='active';

CREATE TABLE media_captions (
    media_id          UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    model_id          TEXT NOT NULL,
    prompt_version    TEXT NOT NULL,
    text              TEXT NOT NULL,
    generated_at      TIMESTAMP NOT NULL,
    status            TEXT NOT NULL,
    PRIMARY KEY (media_id, model_id, prompt_version)
);

CREATE TABLE media_embeddings (
    media_id          UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    model_id          TEXT NOT NULL,
    prompt_version    TEXT NOT NULL,
    vector            BLOB NOT NULL,
    dim               INTEGER NOT NULL,
    generated_at      TIMESTAMP NOT NULL,
    status            TEXT NOT NULL,
    PRIMARY KEY (media_id, model_id, prompt_version)
);

CREATE TABLE ai_active_models (
    task_type         TEXT PRIMARY KEY,  -- embed|tag|caption
    model_id          TEXT NOT NULL,
    prompt_version    TEXT NOT NULL,
    since             TIMESTAMP NOT NULL
);

CREATE TABLE ai_jobs (
    id                          UUID PRIMARY KEY,
    media_id                    UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task_type                   TEXT NOT NULL,  -- embed|tag|caption
    status                      TEXT NOT NULL,  -- pending|working|failed|done
    attempts                    INTEGER NOT NULL DEFAULT 0,
    last_error                  TEXT NULL,
    claimed_at                  TIMESTAMP NULL,
    completed_at                TIMESTAMP NULL,
    model_id_attempted          TEXT NULL,
    prompt_version_attempted    TEXT NULL
);

CREATE UNIQUE INDEX ai_jobs_active_idx
  ON ai_jobs(media_id, task_type)
  WHERE status IN ('pending','working');

CREATE TABLE ai_reindex_failures (
    media_id          UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task_type         TEXT NOT NULL,
    model_id          TEXT NOT NULL,
    prompt_version    TEXT NOT NULL,
    last_error        TEXT NOT NULL,
    failed_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (media_id, task_type, model_id, prompt_version)
);

-- FTS5 lexical index
CREATE VIRTUAL TABLE media_fts USING fts5(
    media_id UNINDEXED,
    filename,
    caption_text,
    tag_labels,
    camera,
    lens,
    location_label
);
-- triggers to keep media_fts in sync with media + media_captions(active) + media_tags(active)
-- are added in implementation; spec records the sync contract, not the SQL.

-- non-secret per-user settings
CREATE TABLE user_settings (
    principal_hub        TEXT NOT NULL,
    principal_user_id    TEXT NOT NULL,
    key                  TEXT NOT NULL,
    value                TEXT NOT NULL,  -- json
    updated_at           TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id, key)
);
```

The matching `000001_initial_schema.down.sql` mirrors all of the above as `DROP TABLE`, `ALTER TABLE … DROP COLUMN`, `DROP INDEX`, and `DROP TRIGGER` in reverse order.

### 12.3 New packages

- `internal/ai/gateway/` — `ModelGateway` interface + `OpenAICompatible` HTTP impl. Two configurable endpoints (`embeddings`, `vision`). Tokens server-side only. Health checks per endpoint.
- `internal/ai/worker/` — three workers (embed, tag, caption) following the existing thumb-worker shape: `Run(ctx)` ticks + `RunOnce(ctx)` claims a batch from `ai_jobs` (`pending`/`working` partial unique index), calls gateway, writes results atomically per the universal artifact rule.
- `internal/searchindex/` — `SearchIndex` interface (semantic) + `LexicalIndex` interface (FTS5). One brute-force search impl, one sqlite-vec wrapper (gated by spike). FTS5 wrapper for lexical.
- `internal/geocode/` — reverse-geocode worker (queued, configurable Nominatim-API-shape endpoint, cached forever in `media.location_label`).
- `internal/auth/hidden/` — argon2id passcode verification, opaque-token session lifecycle, lockout state.
- `internal/maptiles/` — tile-proxy handler with hardening (z/x/y bounds, template-only substitution, no general fetcher), in-memory LRU cache.

### 12.4 API additions (consolidated)

- `POST /api/v1/search`
- `GET /api/v1/media/geo`
- `GET /api/v1/map/tiles/{z}/{x}/{y}`
- `POST /api/v1/auth/hidden/{setup|change|disable|challenge|lock}`
- `GET /api/v1/auth/hidden/status`
- `PATCH /api/v1/albums/{id}`
- `POST /api/v1/albums/{id}/media:reorder`
- `POST|DELETE /api/v1/albums/{id}/pin`
- `GET|PUT /api/v1/settings/user/{key}` (non-secret prefs only)
- `GET /api/v1/settings/{ai|map|geocoder}/status` (config status, no secrets)
- `GET /api/v1/ai/health` (shell-strip dot source)
- Existing `GET /api/v1/shares` (verify; add if missing)

### 12.5 Settings persistence

- **Secrets stay in TOML config + environment overrides.** AI server tokens, map API keys, reverse-geocoder tokens. SPA can neither read nor write them. Settings UI shows status only.
- **Non-secret prefs in SQLite** (`user_settings` table). Theme, density per-context, sort preferences, AI Diagnostics toggle, sidebar pin counts, etc. Endpoint: `GET|PUT /api/v1/settings/user/{key}`.

### 12.6 Cross-cutting privacy enforcement

- Every photo-returning endpoint applies `WHERE hidden_at IS NULL` unless the request carries the `__Host-fotobank-hidden` cookie + `include_hidden=true`. Without unlock, `include_hidden=true` returns 403.
- **Byte endpoints enforce too:** `/original`, `/thumb`, `/preview`, `/large` reject hidden-target media unless the unlock cookie is present.
- **Coords on the wire:** owner-side JSON includes raw `latitude`, `longitude`. Grantee-side JSON includes `location_label` only — raw coords stay owner-only.
- **EXIF GPS in downloadable originals is not redacted in v1.** Documented limitation: grantees who download originals can extract GPS via EXIF. Per-share EXIF stripping = v2.
- **Tile proxy hardening:** z/x/y bounds; template-only substitution; geo `cap` query bounded by server constant ≤ 25 K; refuses non-template URLs; not a general fetcher.

### 12.7 Observability

- AI worker queue depth, throughput, latency, failures → existing `obs.Metrics` shape (mirrors thumb / share workers). The `GET /api/v1/ai/health` endpoint aggregates these.
- `/readyz` does **not** depend on AI servers being reachable — AI is optional infrastructure. It does depend on the migrated schema and auth tables being readable.
- Tile proxy and reverse-geocoder failures emit obs counters; surfaces in Settings panels for the relevant feature.

### 12.8 New sentinel errors (`internal/errs/`)

- `ErrHiddenLocked` → 401 (or 403 generic per the privacy rule)
- `ErrTileSourceUnconfigured` → 503
- `ErrInferenceUnavailable` → 503
- `ErrGeocoderUnconfigured` → 503

Mapped via `httpapi.Translate`.

## 13. Ops — build, dev, testing, theme

### 13.1 Build / dev (middleman pattern)

- `frontend/` directory at repo root: Vite + Svelte 5 + TypeScript. **Bun** as the package manager.
- `internal/web/embed.go` does `//go:embed dist`; `frontend/dist/` is copied to `internal/web/dist/` by `make frontend`. Stub `internal/web/dist/stub.html` keeps `embed.FS` non-empty pre-first-build.
- Make targets:
  - `make frontend` — `bun install && bun run build`, copy `dist/` → `internal/web/dist/`.
  - `make build` — `make frontend` then `go build`. Single binary out.
  - `make dev` — air-driven backend live-reload (`.air.toml` excludes `frontend/`, `internal/web/dist/`, `tmp/`).
  - `make frontend-dev` — `./scripts/frontend-dev.sh` runs `bun run dev` (Vite) with `/api` proxy to backend. User runs `make dev` and `make frontend-dev` in two terminals.
  - `make frontend-check` — `svelte-check`, `tsc --noEmit`, ESLint, Vitest. Frontend safety net.
  - `make api-generate` — extend the existing recipe: dump `openapi.json` (already exists) → `bunx openapi-typescript ... -o frontend/src/api/generated/schema.ts` → write a thin `openapi-fetch` client wrapper.
- `frontend/vite.config.ts` mirrors middleman: dev proxy to backend, `host=127.0.0.1`, port `5181` (distinct from middleman's 5174 so both can run together).
- No `packages/ui` workspace in v1.

### 13.2 Frontend dependencies (v1)

- `svelte`, `vite`, `typescript`, `@sveltejs/vite-plugin-svelte` — pin exact versions in `frontend/package.json` at scaffold time, mirroring middleman's current versions. Bump only when Svelte tooling requires newer.
- `openapi-fetch`, `openapi-typescript`.
- `maplibre-gl` — lazy-loaded only when Map view opens.
- `vitest`, `@testing-library/svelte`, `@playwright/test`, `playwright`.
- `eslint`, `eslint-plugin-svelte`, `prettier` — for ecosystem fit with `svelte-check`; matches middleman.
- **No `dompurify`.** AI text (captions, tags) is rendered as text nodes only — never `{@html}`. Sanitization isn't needed if HTML interpolation never happens.

### 13.3 Testing

- **Unit (Go):** existing `testify/require` pattern.
- **Unit (frontend):** `vitest` + `@testing-library/svelte`.
- **E2E:** Playwright. `cmd/e2e-server` builds a fotobank with test config (**temp SQLite file**, not `:memory:` — `db.Open` creates separate RW/RO pools and opens RO via `mode=ro`; in-memory will not behave like prod). Fixture media seeded.
- **AI worker tests:** mock `ModelGateway` + table-driven tests + fault injection for backoff verification (mirrors `internal/shareworker/worker_test.go` pattern).
- **Visual regression:** deferred to v2.

### 13.4 Theme / typography

- CSS-variable-based theme (agentsview / middleman pattern). Variables in `frontend/src/app.css`: `--bg-primary, --bg-surface, --bg-elevated, --text-primary, --text-secondary, --text-muted, --accent, --border, --shadow, --radius`.
- Light + dark variants. System default via `prefers-color-scheme`; manual override persisted in `user_settings.theme`.
- Sans (Inter or system stack) + mono (JetBrains Mono or system mono). Minimal type system.

### 13.5 Performance targets

Targets are **measured against a named fixture profile**, not hard pass/fail gates on day one. The fixture profile is "100 K-photo library on a typical developer laptop (Apple silicon, recent Chromium)."

- Initial SPA bundle (gzipped): ≤ 250 KB excluding MapLibre. MapLibre adds ~150 KB lazy-loaded only when Map opens.
- Time to first thumb visible (cold cache): < 500 ms after backend ready.
- Grid scroll: 60 fps target under typical scroll velocity at any density.
- Lightbox open: < 100 ms from click to preview visible (assuming preview thumbnail ready).
- Search latency p95: < 500 ms for hybrid query + filters.
- AI worker latency budgets depend on the user's inference servers; not the SPA's responsibility.

### 13.6 Accessibility commitments

- All interactive elements keyboard-reachable.
- Status indicators (AI, Share, Hidden): color + icon + `aria-label`. Color alone never sufficient.
- Lightbox keyboard ladder is the primary path; mouse/touch are equivalent paths.
- Screen reader: alt-text uses `caption_text` when active; falls back to `filename` or `Photo from {date}` when no caption exists.
- Focus management: lightbox traps focus while open; restores to grid cell on close.

## 14. Open spikes (resolve before plan)

1. **Vector index choice.** sqlite-vec extension via `modernc.org/sqlite/vec` vs BLOB+brute-force. Spike measures perf at 50 K and 200 K embeddings; result amends Section 7.
2. **Server-side cluster algorithm.** supercluster-go (port of Mapbox supercluster) vs naive grid-hash. Spike measures perf at 25 K geotagged points; result amends Section 5.3.
3. **Tag confidence calibration.** Are VLM tag scores comparable across models? If not, promotion rule (Section 8.3) is rank-based only (top-K), not score-floored.

## 15. Out-of-scope / v2 candidates (named)

- Face detection / "People" tab.
- Smart album suggestions / "Memories" / "For You".
- AI image editing (auto-enhance, magic eraser).
- Multi-modal prompts.
- Encryption-at-rest for hidden originals.
- Public-link sharing (token-in-URL).
- Multi-account switcher.
- "Shared with me" browse surface.
- Watched-folder / auto-import UI.
- True mobile-first design.
- Custom album-index ordering.
- Auto-revoke sweep for expired shares.
- Per-share EXIF stripping on download.
- Trash / soft-delete for media originals via UI (CLI-only in v1; UI deferred until recovery story exists).
- Photo-cell share badge (deferred until `media.shared_count` projection).
- Drag-photos-onto-sidebar to add to album.
- Editable / regen-able captions.
- WebAuthn / passkey for Hidden re-auth.
- Last-viewed-position restore for Library landing.
- Visual regression testing.
- AVIF/WebP thumb pipeline (defer until pure-Go encoder is production-grade).
- Transcoded video preview tier.

## 16. Forward compatibility — viewer-only accounts

The master vision describes Phase 2.5+ "viewer-only accounts" managed by an external service. v1 is owner-only, but several design decisions reserve room without paying for the feature:

- Identity strip in shell strip is a stub today; layout slot reserved for viewer-only context indicator.
- Recipient input in share dialog takes opaque `(grantee_hub, grantee_user_id)` — broker is the resolver, fits future grantee accounts unchanged.
- Photo-returning endpoints already filter by caller principal; "switch to viewer context" becomes a routing decision, not a refactor.
- Grantee-side JSON payloads already exclude raw GPS coords (per privacy enforcement); viewer-only views inherit this without further work.

## 17. Verification gaps for the plan

The implementation plan should verify these before assuming:

- `/api/v1/originals/...` Range header support — present per `internal/httpapi/originals.go`; verify end-to-end via Playwright video tests.
- `GET /api/v1/shares` exists; if not, add per Section 11.8.
- `scopes.expires_at` column exists; if not, add per Section 11.4.
- Existing thumb pipeline can produce 2560 px and 4096 px tiers (vipsthumbnail-equivalent in pure Go).
- Reconciler / backfill task can re-extract EXIF for existing media (GPS retrofitting).
