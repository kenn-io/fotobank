# F2.3 — Albums + Owner-side Sharing (Web)

> Sub-plan in the F2.x sequence: F2.0 Viewer Foundation → F2.1 GPS Metadata
> → F2.2 RAW + JPEG Pairing → **F2.3 Albums + Owner-side Sharing** →
> F2.4 Hidden Privacy → F2.5 Lightbox Viewer.
> F2.3 **requires F2.2** for the sidecar contract: `MediaActions`
> branches on `media.paired_with_id` (sidecar direct page does not
> mount the cluster), and the AddToAlbum / Share modals expect the
> `AlbumService.AddMedia` and `ShareService.Create` sidecar
> rejections that F2.2 added. F2.3 is independent of F2.4 and may run
> in parallel with it. F2.5's lightbox depends on F2.3 for the action
> cluster (Add to album, Share). Grantee-side viewing (`/shared/*`
> routes, header-mode identity entry) is **out of scope** here and
> deferred to a later sub-plan once a real cross-hub sharing target
> exists.

## §1 Goal

Ship the web/SPA layer for the existing albums and owner-side sharing
backends. The user wants to:

- Browse, create, rename, and delete albums.
- Add and remove photos in albums (single-photo from MediaDetail; bulk
  from a route-aware ActionBar; whole-group via a new
  Select-group affordance on every `MonthChunk` header).
- Create shares for a selected media set or a live album, manage them
  in a `/shares` table, retry failed shares, and revoke active ones.
- Sort album media by capture time (the curation-friendly default) or
  by added-at order.

The deliverable is the `/albums`, `/albums/:id`, and `/shares` web
routes, the action cluster `MediaActions.svelte` consumed by both
MediaDetail and ActionBar, and a small set of backend additions
(album sort mode, share `target_summary`, share list `next_offset`).

## §2 Context

### §2.1 What's already built

**Backend** (`internal/album/`, `internal/share/`, `internal/broker/`,
plus `internal/service/album_service.go` and
`internal/service/share_service.go`): full CRUD, scope state machine,
broker outbox worker, error sentinels, CLI parity. F2.2 patched the
sidecar contract on both surfaces (`AlbumService.AddMedia` and
`ShareService.Create` reject sidecars; `share.Repo.CoverMediaByScopes`
covers paired sidecars whose primary is in scope).

**HTTP routes already registered**:

- Albums (8): `GET /api/v1/albums`, `POST /api/v1/albums`,
  `GET /api/v1/albums/{id}`, `PATCH /api/v1/albums/{id}`,
  `DELETE /api/v1/albums/{id}`, `GET /api/v1/albums/{id}/media`,
  `POST /api/v1/albums/{id}/media`,
  `DELETE /api/v1/albums/{id}/media/{media_id}`.
- Shares (6): `GET /api/v1/shares`, `POST /api/v1/shares`,
  `GET /api/v1/shares/{uuid}`, `POST /api/v1/shares/{uuid}/revoke`,
  `POST /api/v1/shares/{uuid}/retry`,
  `GET /api/v1/shares/{uuid}/preview`.

**Frontend foundation** (from F2.0 + F2.1 + F2.2): SPA router with
`handleInternalLinkClick`, three-column layout, sidebar with
Library/Sessions, `MediaStore` with month-bucketed reactive state,
`VirtualGrid` + `MonthChunk` + `StickyMonthBar` + `YearScrubber`,
shared `SelectionStore`, `ActionBar` shell (renders "X selected" + Done,
no actions yet), MediaCell, MediaDetail with sidecar-direct page +
Files row.

### §2.2 What's missing

- No `/albums`, `/albums/:id`, `/shares` routes.
- No surface for adding/removing photos in albums.
- No surface for creating, listing, retrying, or revoking shares.
- No bulk-select-by-group affordance — multi-select today is
  per-cell (shift/ctrl/meta) only; selecting a whole month or a whole
  session is a manual click-the-first-then-shift-click-the-last grind.
- ActionBar has no actions wired and is not route-aware — it currently
  shows on every route once selection is non-empty.

### §2.3 Stale CLAUDE.md note

`CLAUDE.md` lists Plan D as "in design" and Plan E as "deferred". This
note is stale: both plans landed and have been patched as recently as
the F2.2 sweep. F2.3 will refresh the note in the implementation plan.

## §3 Scope

**In scope**: Option B from brainstorming — albums (full CRUD + media
membership) + owner-side sharing (create, list, retry, revoke). The
sharing UI surfaces a "pragmatic" subset of the backend state machine:
explicit Retry button on Failed shares, label input, AllowDownload
toggle, `BrokerLastError` shown on failed rows, `revoked_remote`
collapsed into a single "Revoked" pill. No expiry picker. No grantee
display-name lookup beyond what the existing list DTO already provides
(`grantee_handle`).

**Out of scope** — deferred to dedicated sub-plans when a real use case
materializes:

- Grantee-side viewing: `/shared/*` routes, header-mode identity entry,
  display-handle UX. The whole "I am a grantee, show me what someone
  else shared with me" surface waits until there's a second fotobank
  instance to consume from.
- Manual album cover thumb selection (today auto-derived in SQL).
- Manual photo reorder within an album (backend has no reorder column).
- Drag-and-drop add-to-album.
- Bulk revoke / bulk retry on `/shares`, search/filter by grantee.
- Expiry picker on shares.
- Lasso / drag-select on the grid.
- Select-all-across-page (multi-month).
- Mobile / touch-specific UX (consistent with the F2.x cadence — F2.5
  carries the mobile pass).

## §4 Design overview

```
                ┌─────────────────────────────────────────────────────┐
                │                    SPA shell                       │
                │   sidebar → BROWSE / CURATE / MANAGE                │
                └─────────────────────────────────────────────────────┘
                          │            │             │
              ┌───────────┘            │             └────────────┐
              ▼                        ▼                          ▼
      /library                 /albums                       /shares
      /sessions                /albums/:id                   (drawer for detail)
      /media/:id

   route-aware <ActionBar>                no <ActionBar>
   per-route action set:                  no <ActionBar>
   - Library, Sessions: Add to album, Share
   - Album detail: Add to album, Share, Remove from this album


   New stores                            Reused
   - AlbumsStore                          - MediaStore
   - AlbumDetailStore                    - SelectionStore (extended)
   - SharesStore (list-only by default)  - DensityStore

   Backend additions
   - AlbumMediaFilter.SortBy = "taken"   (NULLS LAST)
   - scopeDTO.target_summary             ({label, item_count?})
   - listSharesOutput.next_offset        (limit+1 pattern)
```

`MediaActions.svelte` is the F2.5-required action cluster: a small
component that renders Add-to-album / Share buttons (plus Remove on
album-detail context) and, on click, opens the appropriate modal with
the media IDs the parent passed in. MediaDetail mounts it in its
header with `[id]`. ActionBar mounts it (in bulk form) with
`Array.from(selection.ids)`. The same `AddToAlbumModal` and
`ShareModal` serve both invocation paths.

## §5 Routes & sidebar

### §5.1 New routes

| Path             | Component         | Purpose                                  |
|------------------|-------------------|------------------------------------------|
| `/albums`        | `AlbumsIndex`     | Cover-grid of all albums.                |
| `/albums/:id`    | `AlbumDetail`     | Single-album view with media grid.       |
| `/shares`        | `SharesList`      | Owner's outbound shares table + drawer.  |

All three integrate with the existing router (`router.svelte.ts` and
`handleInternalLinkClick`) so reload, deep-link, and Cmd-click-open-in-
new-tab all work.

### §5.2 Sidebar grouping

`Sidebar.svelte` renders three labelled groups:

- **BROWSE** — Library, Sessions
- **CURATE** — Albums
- **MANAGE** — Shares

Each group has a small uppercase muted header above its entries (CSS:
`text-transform: uppercase`, `font-size: 11px`, `color: --text-muted`,
margin-top to separate). Entry markup unchanged: `<a>` with
`onclick={handleInternalLinkClick}`.

`/albums/:id` keeps the **CURATE → Albums** entry highlighted (same
mechanism MediaDetail uses today to keep Library highlighted).

## §6 Stores

### §6.1 `AlbumsStore`

```ts
class AlbumsStore {
  albums = $state<AlbumListItem[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  private nextOffset: number | null = 0;

  async loadInitial();           // fetches page 1
  async loadMore();              // fetches next page (limit=100)
  async create(name: string);    // POST /api/v1/albums; refetch page 1
  async rename(id, name);        // PATCH; merge returned DTO into list
  async delete(id);              // DELETE; remove from list (handles 409)
  byId(id: string): AlbumListItem | undefined;
}
```

Calls `GET /api/v1/albums?limit=100&offset=...`; backend default is 100,
max 1000. Pagination via response `next_offset`; load-more sentinel in
the page (intersection observer at the bottom of the grid).

After successful create, the store fetches page 1 (not local prepend),
because `AlbumService.List` ordering is backend-owned and the cover
field is SQL-derived — the client should not guess.

### §6.2 `AlbumDetailStore`

```ts
class AlbumDetailStore {
  album = $state<Album | null>(null);
  itemIds = $state<string[]>([]);   // ordered list, frozen by sort
  itemCount = $state(0);
  loading = $state(false);
  exhausted = $state(false);
  sort = $state<"taken" | "added">("taken");
  private nextOffset: number | null = 0;

  async load(id: string);           // GET /api/v1/albums/{id}
  async loadMore();                 // GET /api/v1/albums/{id}/media?...
  async setSort(next);              // resets offset, refetches
  // Bulk-remove with bounded parallelism (4) and partial-failure handling.
  async removeMany(ids: string[]): Promise<{succeeded: string[]; failed: string[]}>;
  // Computed: read-only Set for selection ∩ membership.
  hasInAlbum(id: string): boolean;  // O(1) lookup via internal Set
}
```

Constructed with a reference to the shared `MediaStore`. Each page of
album media is merged into `MediaStore` via `mergeRaw()`; the album's
order lives separately in `itemIds`. Reading a row by id goes through
`MediaStore.get(id)` so Library and Album see the same `Media` record.
This means a thumb-version bump from the worker propagates to both
views simultaneously.

### §6.3 `SharesStore`

```ts
class SharesStore {
  scopes = $state<ScopeListRow[]>([]);   // list-shape DTO, no media_ids
  loading = $state(false);
  exhausted = $state(false);
  showRevoked = $state(false);
  private nextOffset: number | null = 0;
  private pollHandle: number | null = null;
  private detailCache = new Map<string, ScopeDetail>();
  private previewCache = new Map<string, SharePreview>();

  async loadInitial();
  async loadMore();
  async create(input);                // POST /api/v1/shares
  async revoke(uuid);                  // POST /revoke
  async retry(uuid);                   // POST /retry
  async getDetail(uuid);               // GET /shares/{uuid}, cached
  async getPreview(uuid);              // GET /shares/{uuid}/preview, cached
  setShowRevoked(v: boolean);          // refetches with IncludeSettled
  startPolling();                      // 5s interval while pending|revoking
  stopPolling();
}
```

Polling auto-starts in `loadInitial` and `create`/`retry`/`revoke` if
the resulting list contains any `pending` or `revoking` row. It
auto-stops when all rows are settled. Each tick refetches `limit=200`
rows from offset 0 and **merges by uuid into the existing list** — it
never replaces the list, so rows the user already paginated into stay
put. If the user has loaded more than the poll's limit, the poll only
refreshes status for the first 200 rows; older rows update on the next
user-driven `loadMore` (acceptable: stale settled rows do not change
state, and pending rows are almost always recent).

Detail and preview caches are populated lazily when the drawer opens.
They are cleared on revoke/retry success (the data has changed).

### §6.4 `SelectionStore` extensions

The existing `SelectionStore` (`ids: SvelteSet<string>`, `lastAnchor`)
gains:

- `addAll(ids: Iterable<string>)` — adds; updates `lastAnchor` to the
  last id in the iterable (range-select anchor moves so a subsequent
  shift-click extends from the group).
- `removeAll(ids: Iterable<string>)` — removes; leaves `lastAnchor`
  untouched (so a shift-click after deselect-group still does what the
  user expected).
- `hasAll(ids: Iterable<string>): boolean` — returns `false` for empty
  input (so empty chunks don't render "Deselect").

No internal restructure: still a single `SvelteSet` underneath.

## §7 VirtualGrid + MonthChunk extensions

### §7.1 `VirtualGrid` flat mode

`VirtualGrid` grows one optional prop:

```ts
{ months, onLoadMore, targetRowHeight, timelineChrome = true }
```

When `timelineChrome={false}`:

- `StickyMonthBar` is not rendered.
- `YearScrubber` is not rendered.
- VirtualGrid forwards `label={undefined}` to `MonthChunk` (suppressing
  the `.day-header` — current `MonthChunk` markup is
  `{#if label}<header class="day-header">{label}</header>{/if}`,
  so dropping the label drops the header). Album views must not
  render a header that says `album:<id>`.
- The `each` over `months` still runs; it just degenerates to a single
  `MonthChunk` for album views (the album feeds a single synthetic
  `Month` with `key="album:<id>"`).

This is the smallest change that unblocks album-detail without
introducing a `groupBy` abstraction. CSS for the grid container
unchanged.

### §7.2 `MonthChunk` `headerAction` slot

`MonthChunk` grows one optional prop. Existing prop name is `options`
(of type `LayoutOptions`), not `layout`:

```ts
{ label, items, options, renderCell, headerAction?: Snippet }
```

When `headerAction` is supplied, the `.day-header` renders the snippet
to its right. Library and Sessions both pass a `GroupSelectButton`
snippet that captures the chunk's media IDs. Album detail does not
pass one (album header lives in `AlbumDetail.svelte`, not in the
grid).

The header itself remains non-interactive (no whole-bar click target).
Hover styling lives on the button only; the header's role stays
`heading` (or whatever it is today — confirm during implementation).

`StickyMonthBar` does **not** grow this affordance. It is `aria-hidden`
and only mirrors the active sticky label; it does not track the active
month's IDs and giving it actions would create dual sources of truth
for "which month am I currently selecting".

## §8 Albums browse (`/albums`)

### §8.1 Layout

Header: page title "Albums" (left) + "+ New Album" button (right).
Body: CSS-grid of square cover tiles, `auto-fill, minmax(180px, 1fr)`,
gap 12px.

Each tile:
- Anchor: `<a href="/albums/{id}" onclick={handleInternalLinkClick}>` —
  full-tile click target so reload, Cmd-click new-tab, middle-click all
  work.
- Cover thumb: 1:1 square (`aspect-ratio: 1 / 1`) via
  `/api/v1/media/{cover.media_id}/thumb?size=grid&v={cover.thumb_version}`.
- Album name (single-line, ellipsized).
- Item count (muted, e.g. "12 photos").

Square covers chosen for **scan rhythm**: tiles are repeated inventory,
not photo inspection; uneven heights would create visual noise without
helping recognition. Cover thumb is `object-fit: cover` so non-square
sources don't letterbox.

### §8.2 Empty + placeholder states

**Empty album** (`Cover === null`): tile renders with placeholder
background using existing theme variables — `--bg-elevated` for the
tile body, `--border` for the outline, a small icon centered in
`--text-muted`. Album name and "0 photos" still visible.

**Empty grid** (no albums at all): centered "No albums yet" + "Create
your first album" button. Button opens the same New Album modal as the
"+ New Album" header button — no separate flow.

**Loading state**: skeleton tiles for the first load; sentinel
"Loading…" text for subsequent pages.

### §8.3 New Album modal

Triggered from header button OR empty-state button. Modal contents:

- Title: "New album"
- Single text input, label "Name", autofocus, `maxlength=200`
- Inline validation: trimmed length ≥1; show error on blur or submit
- Buttons: Cancel (escape), Create (Enter when valid)

On submit: `POST /api/v1/albums` with `{name}`. On success, modal
closes, AlbumsStore refetches page 1, the new album appears in the
grid wherever the backend placed it. On 400 (`ErrInvalidName`): inline
error. On 5xx: toast "Failed to create album" + leave modal open.

(Album names are not unique in the current schema/service, so there
is no 409 case here. Two albums with the same name are legal; their
UUIDs and `updated_at` timestamps disambiguate.)

### §8.4 Pagination

Initial load `limit=100, offset=0`. The grid contains an intersection-
observer sentinel at its bottom; firing it calls
`AlbumsStore.loadMore()`, which uses `nextOffset` from the previous
response. Stops when the response returns `next_offset: null`.

### §8.5 Out of scope at the index

- Sort / filter / search (backend supports neither beyond limit/offset).
- Rename / delete from the index (lives only on detail).
- Drag-and-drop reorder of albums (no backend column).
- Bulk operations on albums.

## §9 Album detail (`/albums/:id`)

### §9.1 Layout

Header strip:
- Title row: album name (large) + count beside it (muted, e.g.
  "12 photos").
- Action row: `[Rename] [Delete] [Share album]` (left) and
  `Sort: [Date taken ▾]` (right).

Body: `<VirtualGrid timelineChrome={false}>`. AlbumDetailStore feeds
it a single synthetic `Month`:

```ts
months = [{ key: `album:${id}`, items: orderedMedia }]
```

Each item is the same `Media` shape stored in `MediaStore` (rows
merged via `mergeRaw` on each page load). VirtualGrid renders a
single `MonthChunk` with no header: in `timelineChrome={false}` mode
VirtualGrid forwards `label={undefined}` (per §7.1) so MonthChunk's
`{#if label}` guard skips rendering `.day-header`. No `headerAction`
snippet is supplied either; the album header lives in the route's
own header strip above the grid.

ActionBar mounts on this route with action set
`[AddToAlbum, Share, RemoveFromThisAlbum]`. The latter is rendered
only when `selectedIds ∩ albumItemIds` is non-empty; it operates only
on that intersection.

### §9.2 Sort

The Sort dropdown toggles between:

- **Date taken** (default) — `sort_by=taken&sort_asc=false`.
- **Recently added** — `sort_by=added&sort_asc=false`.

Default is "Date taken" because albums are a curation surface; the
user's mental model is "show this album in capture order".

Switching sort resets `nextOffset` to 0, clears `itemIds`, and
re-fetches from page 1.

### §9.3 Pagination

`AlbumDetailStore.loadMore()` fetches
`/api/v1/albums/{id}/media?limit=200&offset=...&sort_by=...&sort_asc=...`.
Intersection-observer sentinel at the bottom of the grid triggers
load-more. Stops when `next_offset === null`.

### §9.4 Rename

Triggered from header button. Opens a modal with the current name
pre-filled. `PATCH /api/v1/albums/{id}` returns the updated `album DTO`;
the store updates `album` from the response (not optimistic — wait for
the server to confirm and use its DTO). On 400 / 409: inline error.

### §9.5 Delete

Triggered from header button. Confirmation modal:

> Delete '<name>'? Photos remain in your library.

On confirm: `DELETE /api/v1/albums/{id}`. Success → route to `/albums`
(via the router). On 409 (`share.ErrAlbumHasLiveScopes`): close the
confirmation modal and toast:

> This album has active shares. Revoke them in Shares first.
> [View shares →]

The toast link routes to `/shares?album_id=<id>` (filter applied; see
§11.4). No revoke-from-this-dialog flow — that crosses too many
domains. The user goes to /shares, revokes, comes back.

### §9.6 Share album

Triggered from header button. Opens `ShareModal` pre-set to
`{target_type: "album_live", album_id: id, label: "", allow_download: false}`.
(Backend create body uses `album_id`; the response surfaces it as
`target_album_id` on `scopeDTO` — see `internal/httpapi/shares.go:154` for
the request shape and `:67` for the response.)
See §12.

### §9.7 Bulk Remove

ActionBar's "Remove from this album" action operates on the route-scoped
intersection:

```ts
const selectedInAlbum = Array.from(selection.ids)
  .filter((id) => albumDetailStore.hasInAlbum(id));
```

Action only renders when `selectedInAlbum.length > 0`. Clicking it:

1. Calls `AlbumDetailStore.removeMany(selectedInAlbum)`.
2. `removeMany` issues `DELETE /api/v1/albums/{id}/media/{media_id}`
   with concurrency 4 via `Promise.allSettled`.
3. Successful IDs are removed from `itemIds` locally.
4. Successful IDs are removed from the global selection
   (`selection.removeAll(succeeded)`); other (non-album) selections are
   preserved.
5. On any failures: refetches the current page to repair state and
   toasts "Removed N photos · M failed".
6. On full success: toast "Removed N photos".

### §9.8 Empty state

```
Empty album.
Select photos in Library, then use Add to album in the action bar.
```

### §9.9 Out of scope here

- Manual photo reorder, manual cover selection, info panel inside the
  album, drag-to-add. All deferred.

## §10 Add-to-album modal (`AddToAlbumModal.svelte`)

### §10.1 Trigger

Opened from `MediaActions` Add button:
- MediaDetail single: `mediaIds = [media.id]`.
- ActionBar bulk on Library / Sessions: `mediaIds = Array.from(selection.ids)`.
- ActionBar bulk on Album detail:
  `mediaIds = selectedInAlbum` (the route-scoped intersection
  `Array.from(selection.ids).filter(id => albumDetailStore.hasInAlbum(id))`,
  same rule the album route applies to Remove and Share — see §9.7,
  §11/§12). Same-album-as-target adds are legal; the backend's
  `already_present` counter handles them.

The modal is constructed with `{mediaIds: string[]}` only; it has no
view of the global selection.

### §10.2 Layout

- Title: "Add to album"
- Subtitle: count line — "1 photo" / "12 photos"
- Search input: `placeholder="Search albums..."`, autofocus
- "+ Create new album" row (sticky at the top of the list)
- Scrollable list of album rows: cover (32px square thumb) + name +
  `(item_count)` muted; clicking selects the row (highlight)
- Primary button: **Add 12 photos** (label reflects `mediaIds.length`;
  disabled until a target album is selected)
- Cancel button

### §10.3 Selection (in-modal)

Two-step interaction by design:
1. User clicks a row → row highlights, primary button enables.
2. User clicks primary → request fires.

Single-step "click row to add" was rejected because a misclick on a
12-photo selection would push 12 photos into the wrong album with no
undo. The two-step is consistent with destructive-confirmation
ergonomics and gives the create-new flow the same accident-avoidance.

### §10.4 Create-new flow

Clicking "+ Create new album" replaces the search/list area with:
- Name input (`maxlength=200`, autofocus)
- "Create" button (disabled until name is non-empty after trim)
- "Cancel" link (returns to the album list)

On Create: `POST /api/v1/albums`. On success, the new album becomes the
**selected target** in the modal, and the modal returns to the list
view with the new album highlighted at the top of the list. The
primary button is now enabled. The user must still click it to fire
the add — accident avoidance per §10.3.

### §10.5 Search filter

Client-side only (no debounce, no server search). Albums are typically
under 100. Filtering uses
`name.toLocaleLowerCase().includes(q.toLocaleLowerCase())` via a small
helper `normalizeForSearch(s: string)`. No NFC/NFD work — the corpus
is small, the helper exists only to centralize trim + lowercase.

### §10.6 Submit

`POST /api/v1/albums/{target.id}/media` with `{media_ids: [...]}`.

The backend caps `BatchMaxLen=500` per call. The client chunks
client-side: if `mediaIds.length > 500`, split into chunks of 500 and
fire with concurrency 4 via `Promise.allSettled`.

Response per chunk: `{added, already_present}`. Aggregate across
successful chunks:

```ts
const totals = {
  added: chunks.reduce((sum, c) => sum + (c.ok ? c.body.added : 0), 0),
  already: chunks.reduce((sum, c) => sum + (c.ok ? c.body.already_present : 0), 0),
  failedChunks: chunks.filter((c) => !c.ok).length,
};
```

### §10.7 Result handling

**All chunks succeeded**:
- Toast text:
  - `added > 0 && already === 0` → "Added 12 photos to **Italy 2025**"
  - `added > 0 && already > 0` → "Added 9 photos to **Italy 2025** · 3 already present"
  - `added === 0 && already > 0` → "All 12 photos already in **Italy 2025**"
- Modal closes.
- Selection cleared (was passed-in selection).
- AlbumsStore refetches page 1 in the background to refresh item counts.
- If the user is currently on `/albums/:id` for the target album,
  `AlbumDetailStore` refetches.

**Any chunk failed**: modal stays open, selection NOT cleared, banner
in modal shows "N chunks failed; try again". User can hit primary
again. The store does not retry automatically — the user is in
control.

### §10.8 Error mapping

- 404 album: toast "Album no longer exists" + close modal + AlbumsStore.refetch.
- 400 `ErrInvalidArgument` (sidecar slipped through; defense — should
  be impossible since sidecars are hidden from Library by default):
  inline modal banner "RAW sidecars cannot be added to albums."
  Stop processing remaining chunks.
- 503: inline modal banner "Service unavailable. Try again later."
- Network: inline modal banner "Failed to add photos."

## §11 Shares list (`/shares`)

### §11.1 Layout

Header: page title "Shares" + "Show revoked" toggle (off by default;
on flips `IncludeSettled=true` in the API call).

Body: a table-like layout (responsive — collapses to stacked rows on
narrow viewports during F2.5 mobile pass; v1 desktop-only):

| Column     | Source                                                                    |
|------------|---------------------------------------------------------------------------|
| Label      | `scope.label || scope.target_summary.label`                              |
| Type       | "Photos" (media_set) or "Album" (album_live) — pill                      |
| Grantee    | `scope.grantee_handle || ${grantee.hub}:${grantee.user_id}`              |
| State      | Pill (color + icon + aria-label) — see §11.3                             |
| Created    | Relative ("3 days ago") with absolute timestamp on hover                 |
| Actions    | Per-state buttons — see §11.4                                            |

Empty state: "No shares yet. Share photos or albums from the Library
or an album page."

### §11.2 Pagination

`SharesStore.loadInitial` calls
`GET /api/v1/shares?limit=100&offset=0&include_settled=...` and reads
`next_offset` from the response (newly added by this plan; see §15.3).
A "Load more" button at the bottom of the list fires
`SharesStore.loadMore()` when `nextOffset !== null`.

### §11.3 State pill

```
state             color           icon          aria-label
---------------- ---------------- ------------- --------------------
pending          var(--warn)      hourglass     Pending publish
active           var(--ok)        check         Active
failed           var(--danger)    triangle-!    Publish failed
revoking         var(--orange)    spinner       Revoking
revoked          var(--muted)     dash          Revoked
expired          var(--muted)     clock-x       Expired
```

`expired` overrides whatever the broker_status is when
`scope.expires_at && now > expires_at`. The label change is purely
display; broker_status field stays as-is on the wire.

### §11.4 Per-state actions

| State    | Buttons rendered             |
|----------|------------------------------|
| pending  | Revoke                       |
| active   | Revoke                       |
| failed   | Retry, Revoke (cleanup path) |
| revoking | (none — busy)                |
| revoked  | (none — read-only)           |
| expired  | Revoke (if not yet revoked)  |

**Revoke** opens a confirmation modal:

> Revoke share to **<grantee>**?
> Photos will become inaccessible to them.

(`<grantee>` uses `grantee_handle` if present, else `hub:user_id`.)

On confirm: `POST /api/v1/shares/{uuid}/revoke`. Row state flips to
`revoking`. Polling resumes.

**Retry** is a no-confirmation idempotent action (it's a retry of an
already-attempted publish; nothing new is exposed). On click:
`POST /api/v1/shares/{uuid}/retry`. Row goes back to `pending`.

### §11.5 Polling

`SharesStore.startPolling()` fires every 5s while at least one row is
`pending` or `revoking`. Each tick refetches `limit=200` rows from
offset 0 and merges status updates by `uuid` into the current `scopes`
list (no list replacement, no pagination reset). When all visible rows
settle, polling stops automatically. Polling resumes on Create /
Revoke / Retry.

The interval is hardcoded at 5s. No exponential backoff — the worker
is local; the broker is the bottleneck. If polling becomes load-
sensitive, the timer is one place to tune.

### §11.6 Drawer for details

Clicking a row opens a right-side drawer (occupying the existing
detail-column slot in `ThreeColumnLayout`). Drawer contents:

1. Header: label / target_summary.label, state pill (same as row).
2. Lazy-fetched detail block:
   - For `media_set`: count + "Preview" button → fetches
     `/api/v1/shares/{uuid}/preview` and renders a thumb grid
     (read-only; no actions on individual photos).
   - For `album_live`: album name (from `target_summary.label`) + a
     link to `/albums/<target_album_id>`. (No preview fetch needed
     because the live album content is owner-side accessible already.)
3. Metadata: created_at, expires_at (if set; v1 always null until the
   forward expiry-picker plan), broker_attempts (only shown if
   `failed`), broker_last_error (only shown if `failed`),
   allow_download badge.
4. Buttons: same Revoke / Retry as the row.

Drawer dismissal: Escape key, click outside, close button. Selection
state is unaffected.

`SharesStore.getDetail(uuid)` is called on drawer open and cached;
`getPreview(uuid)` only when the user clicks the Preview button.

### §11.7 URL filters

The `/shares` route supports query params:
- `?album_id=<id>` — preset the API call to filter by album_id (used
  by the §9.5 "View shares →" toast link).
- `?show_revoked=true` — preset the toggle.

These are read on mount and written-back as the user toggles state, so
deep-linking and browser-back work.

### §11.8 Errors

- 404 on revoke/retry: row no longer exists; toast "Share no longer
  exists" and remove row.
- 409 `ErrScopeAlreadyRevoked` on revoke: toast "Share is already
  revoked" + refetch list.
- 409 `ErrRetryNotApplicable` on retry: toast "Share is no longer in
  failed state" + refetch list.
- 503: "Sharing is unavailable. Try again later." (No claim about
  broker — 503 means the share service itself is unreachable, not the
  broker; broker-down rows just sit in `pending`/`failed` and the
  worker keeps trying.)
- 5xx on poll tick: log; don't toast (poll noise). Polling continues.

## §12 Share creation modal (`ShareModal.svelte`)

### §12.1 Trigger

Opened from `MediaActions` Share button OR album detail "Share album":

| Caller                      | target_type    | Body fields                                          |
|----------------------------|----------------|------------------------------------------------------|
| MediaDetail (single photo)  | media_set     | `media_ids: [media.id]`                              |
| Library/Sessions ActionBar  | media_set     | `media_ids: Array.from(selection.ids)`               |
| Album detail ActionBar      | media_set     | `media_ids: selectedIds ∩ albumItemIds`              |
| Album detail header         | album_live    | `album_id: id` (request); `target_album_id` on response |

The selection ∩ rule for the album-detail-ActionBar Share is the same
rule as Remove from this album (§9.7): respect the route, don't share
unrelated Library selections just because selection is global.

### §12.2 Layout

- Title: context-aware
  - "Share 1 photo" / "Share 12 photos"
  - "Share album: Italy 2025"
- Read-only summary line:
  - "12 photos selected" (media_set)
  - "Live album · 47 items today" (album_live; count from the album)
- Form fields:
  - **Grantee** — single text input, monospace, placeholder
    `myhub:bob`, helper text "hub:user_id"
  - **Label** — optional text input, `maxlength=200`, helper text
    "Helps identify this share later."
  - **Allow download** — checkbox, default off
- Primary button: "Create share" (disabled until grantee parses)
- Cancel button

### §12.3 Validation (client-side)

- **Grantee**: must match `^[^:\s]+:[^:\s]+$`, both segments ≤255
  chars, neither empty. Inline error under the input on blur or submit.
- **Label**: trimmed length ≤200. Inline error.
- **media_set guard**: if `mediaIds.length > 1000`, primary button is
  disabled with helper text "Selection too large (max 1000 per share)".
- **album_live**: no extra client checks; backend `ErrAlbumEmpty`
  surfaces inline if the album has no media.

### §12.4 Submit

The text input is parsed into the API's object shape:

```ts
const [hub, user_id] = grantee.split(":", 2);
body.grantee = { hub, user_id };
```

`POST /api/v1/shares` with `{target_type, label, allow_download,
grantee, [media_ids | album_id]}` (request body uses `album_id`; the
response surfaces it as `target_album_id`). On 201:
- Toast "Share created" with link "View in Shares →" routing to
  `/shares`.
- Modal closes; bulk-path selection cleared (single-photo selection,
  if any, untouched).
- If user is currently on `/shares`, refetch the list (don't prepend —
  the list response carries `target_summary` + `grantee_handle` that
  the create response doesn't necessarily include).

### §12.5 Error mapping (inline modal banner; modal stays open)

- 400 `ErrInvalidGrantee`: "Grantee is empty, oversized, or matches
  your own identity."
- 400 `ErrInvalidMediaSet`: "Selection must be 1..1000 unique photos."
- 400 `ErrAlbumEmpty`: "Album is empty — add photos first."
- 400 `ErrInvalidLabel`: "Label exceeds 200 characters."
- 400 `ErrInvalidArgument` (sidecar in selection — defense; sidecars
  are hidden from Library by default): "RAW sidecars cannot be shared
  directly."
- 503: "Sharing is unavailable. Try again later."
- Generic 5xx / network: "Failed to create share."

## §13 Bulk-select-by-group

### §13.1 The affordance

Every `MonthChunk` header in Library and Sessions gets a small
`GroupSelectButton` to its right. Visual: a ghost button with a
checklist icon and the label "Select group" or "Deselect group"
depending on `selection.hasAll(chunk.itemIds)`. Hover styling lives
on the button only — the surrounding header is non-interactive.

### §13.2 The component

```svelte
<!-- frontend/src/lib/components/GroupSelectButton.svelte -->
<script lang="ts">
  import { selection } from "../selection/selectionStore.svelte";
  let { ids, label }: { ids: string[]; label: string } = $props();
  const allSelected = $derived(selection.hasAll(ids));
  function toggle() {
    if (allSelected) {
      selection.removeAll(ids);
    } else {
      selection.addAll(ids);
    }
  }
</script>

<button
  class="group-select"
  onclick={toggle}
  aria-label={allSelected
    ? `Deselect ${ids.length} photos in ${label}`
    : `Select ${ids.length} photos in ${label}`}
>
  {allSelected ? "Deselect group" : "Select group"}
</button>
```

`label` is the human-readable group name (month "April 2024" or
session "April 23, 2024"). It's used only for the aria-label.

### §13.3 Wiring through MonthChunk

Library uses `VirtualGrid` to render its month chunks; Sessions
constructs `MonthChunk` directly in its own `{#each sessions}` loop
(see `frontend/src/routes/Sessions.svelte`). Both routes need to wire
`headerAction`:

- **Library**: `VirtualGrid` grows a forwarding
  `headerAction?: Snippet<[chunk: Month]>` prop and relays it
  verbatim to every `MonthChunk` it constructs. Library passes the
  snippet on `<VirtualGrid headerAction={...} />`.
- **Sessions**: passes the snippet directly on `<MonthChunk
  headerAction={...} />` inside its existing `{#each sessions}` loop.

Album detail (which uses `timelineChrome={false}` on `VirtualGrid`)
does not pass a `headerAction`; the album route's own header strip
serves that role.

```svelte
<!-- Library.svelte -->
<VirtualGrid months={mediaStore.months} {onLoadMore} {targetRowHeight}>
  {#snippet headerAction(chunk)}
    <GroupSelectButton
      ids={chunk.items.map(i => i.id)}
      label={chunk.label ?? chunk.key}
    />
  {/snippet}
</VirtualGrid>
```

```svelte
<!-- Sessions.svelte -->
{#each sessions as s (s.id)}
  <MonthChunk
    items={...}
    label={`${first.taken.toUTCString().slice(0, 16)} · ${s.items.length} photos`}
    options={...}
  >
    {#snippet headerAction()}
      <GroupSelectButton
        ids={s.items.map(i => i.id)}
        label={`${first.taken.toUTCString().slice(0, 16)} session`}
      />
    {/snippet}
    {#snippet renderCell(m)}
      <MediaCell ... />
    {/snippet}
  </MonthChunk>
{/each}
```

### §13.4 SelectionStore semantics

- `addAll(ids)` — adds every id; sets `lastAnchor` to the **last** id
  in the iterable. Rationale: a subsequent shift-click extends from
  the bottom of the just-selected group, which is what users expect
  ("I selected all of April, now let me extend into May").
- `removeAll(ids)` — removes every id; **does not** touch `lastAnchor`.
  Rationale: deselecting a group shouldn't move the range anchor; the
  next shift-click should extend from wherever the user clicked last.
- `hasAll(ids): boolean` — empty `ids` returns `false`. Otherwise
  returns true iff every id is in the set. O(n) with early exit.

### §13.5 Out of scope

- Lasso / drag-select on the grid surface.
- Range-select across groups (works today via shift-click on cells;
  no group-level UI for it).
- Select-all-everywhere button. Could be added later as a header
  button on Library/Sessions when needed.

## §14 `MediaActions` component

### §14.1 Shape

```ts
type MediaActionsProps = {
  mediaIds: string[];                      // required, may be length 1
  context?: "library" | "session" | "album"; // default "library"
  albumId?: string;                         // when context === "album"
};
```

The component renders buttons:

- **Add to album** (always)
- **Share** (always)
- **Remove from this album** (only when `context === "album"`)

Each button opens its corresponding modal (`AddToAlbumModal`,
`ShareModal`) with the right pre-set context. The component does not
read global selection internally — callers pass `mediaIds` directly.
This makes single-photo MediaDetail use trivial.

### §14.2 Mount points

- **MediaDetail header**: mounts `<MediaActions mediaIds={[media.id]} />`
  with no `context` (default "library"). Renders Add + Share, no Remove.
- **ActionBar on Library / Sessions**: mounts
  `<MediaActions mediaIds={Array.from(selection.ids)} />`. Renders Add + Share.
- **ActionBar on Album detail**: mounts
  `<MediaActions mediaIds={selectedInAlbum} context="album" albumId={id} />`.
  Renders Add + Share + Remove.

The same component, the same modals, three call sites.

### §14.3 Sidecar-on-MediaDetail

When MediaDetail is rendering a sidecar direct page (no primary
photo, just metadata + Download button per F2.2 §12), `MediaActions`
is **not** mounted. Sidecars cannot be added to albums or shared
directly (backend rejects with `ErrInvalidArgument`); the buttons
would all be dead ends.

The branch is in `MediaDetail.svelte` — the existing branching on
`media.paired_with_id` (added in F2.2 Task 12) gates whether
`MediaActions` is rendered.

## §15 Backend changes

Three small additions; all live in the existing `internal/album` /
`internal/share` / `internal/httpapi` packages.

### §15.1 `AlbumMediaFilter.SortBy = "taken"`

Files:
- `internal/album/album.go`: `ErrInvalidSort` message updated to
  list the new value: `"sort_by must be \"added\", \"imported\", or \"taken\""`.
- `internal/album/repo.go`: `ListMedia` ORDER BY for
  `SortBy == "taken"`:

  ```sql
  ORDER BY m.timestamp <dir> NULLS LAST, am.media_id <dir>
  ```

  The `NULLS LAST` qualifier is essential: undated media (rare but
  possible — backfill rows or imports without EXIF) should sort to
  the bottom, not jump above dated photos.

- `internal/service/album_service.go`: validator accepts the third
  value.
- `internal/httpapi/albums.go`: no change; the field is already
  pass-through.
- `internal/album/repo_test.go` + `internal/service/album_service_test.go`:
  new cases — `taken` ASC + DESC, NULL timestamp sorts last in both
  directions, mixed-NULL ordering deterministic via `media_id` tiebreak.

### §15.2 `scopeDTO.target_summary`

Files:
- `internal/share/share.go`: defines a `TargetSummary` struct
  (`{Label string; ItemCount *int}`) — domain type only; no DB lookup
  helpers.
- `internal/service/share_service.go` (and/or `share.Repo`): new
  helper `populateTargetSummary(ctx, scopes []share.Scope) ([]TargetSummary, error)`
  that, for each scope, returns either:
  - `media_set`: `{Label: "<n> photos", ItemCount: &n}` where `n` is
    `share.Repo.CountSharedMediaByScope(uuid)` (existing helper).
  - `album_live`: `{Label: "Album: <name>", ItemCount: nil}` where
    `name` comes from a new `album.Repo.GetNamesByIDs(ids []string)`
    helper or via JOIN in the existing list query.

  Computation lives in service/repo per the brainstorming
  clarification — `internal/share/share.go` is a domain package and
  does not grow DB lookup helpers.

- `internal/httpapi/shares.go`: `scopeDTO` and `scopeDetailDTO` gain a
  new field:

  ```go
  TargetSummary *targetSummaryDTO `json:"target_summary,omitempty"`
  ```

  The `targetSummaryDTO` mirrors the domain type. `toScopeDTO` and
  `toScopeDetailDTO` populate it from the service helper's output.
  List handlers call the new helper after `svc.List`; detail handlers
  call it for the single returned `ScopeDetail`.

- Tests: existing share repo + service tests gain cases for the new
  helper. HTTP tests assert the field appears on list and detail
  responses for both target types.

### §15.3 `listSharesOutput.next_offset`

Files:
- `internal/httpapi/shares.go`: `listSharesOutput.Body` gains a new
  field:

  ```go
  NextOffset *int `json:"next_offset,omitempty"`
  ```

  The list handler already calls
  `clampLimit(in.Limit, sharesListDefaultLimit, sharesListMaxLimit)`;
  switch to the limit+1 pattern used by media and albums:

  ```go
  filter.Limit = limit + 1
  rows, err := svc.List(ctx, filter, caller)
  ...
  hasMore := len(rows) > limit
  if hasMore {
      rows = rows[:limit]
      next := offset + limit
      out.Body.NextOffset = &next
  }
  ```

- Tests: `internal/httpapi/shares_test.go` gains a paginated case
  asserting `next_offset` matches the limit+1 sniff and disappears on
  the final page.

### §15.4 `openapi.json` regeneration

After §15.1–§15.3 are in place, run `make api-generate` (the prek hook
also does this on commit). Frontend regenerates client types from the
new spec.

## §16 Testing strategy

### §16.1 Backend (Go)

- New / extended unit tests in `internal/album/repo_test.go`,
  `internal/service/album_service_test.go`, `internal/share/repo_test.go`,
  `internal/service/share_service_test.go`, `internal/httpapi/albums_test.go`,
  `internal/httpapi/shares_test.go` for the §15 changes.
- Existing trigger / sentinel tests already cover the touched
  surfaces; no new triggers are added.

### §16.2 Frontend unit (vitest)

- `AlbumsStore`, `AlbumDetailStore`, `SharesStore` tests with a fake
  client (existing pattern from `MediaStore`).
- `SelectionStore` `addAll` / `removeAll` / `hasAll` and `lastAnchor`
  semantics.
- `normalizeForSearch` helper.
- Validation helpers: grantee parser, label length, media_set guard.

### §16.3 Frontend component (vitest + happy-dom)

- `AlbumGrid` empty / loading / populated states.
- `AlbumDetail` header rendering, sort change refetches.
- `AddToAlbumModal` two-step interaction (row → enable primary → click
  primary), create-new flow, search filter, chunked add accounting,
  partial-failure modal-stays-open behavior.
- `ShareModal` validation, target context rendering, error banner
  flows.
- `SharesList` row rendering, state pill mapping, action visibility per
  state, expired override.
- `GroupSelectButton` toggle behavior, aria-label correctness.
- `MediaActions` mount-point matrix (single, library bulk, album bulk).

### §16.4 Frontend e2e (playwright)

- **Album round-trip**: create album → add 3 photos via Library
  multi-select → open `/albums/:id` → see them → switch sort → see
  them in order → remove 1 via ActionBar → see 2 → delete album with
  confirmation → back to `/albums` and album is gone.
- **Bulk-select-by-group**: open Library, click "Select group" on a
  month header, see N selected in ActionBar, click "Add to album"
  with a target album, verify N copied.
- **Sidecar gating**: confirm `MediaActions` does not render on a
  sidecar direct page (regression for §14.3).
- **Share round-trip (no broker)**: create a share with grantee
  `noop:bob` → see it in `/shares` as `pending` → wait for
  `failed` (after attempts) → click Retry → still `pending` → click
  Revoke → confirm → eventually `revoking` → polled to settled.
  Uses a stubbed `noop` broker on the e2e server.
- **/shares?album_id=<id> toast deep-link**: delete an album with a
  live scope, confirm the conflict toast appears, click "View shares
  →", verify `/shares` opened with the album_id filter applied.

The e2e fixtures (`cmd/e2e-server/main.go`) gain a few seed albums
and a couple of stub shares.

## §17 Forward dependencies

- **F2.4 Hidden Privacy** — independent. F2.4 may add a "Hidden"
  contextual entry to the sidebar's BROWSE group. Its action cluster
  shares `MediaActions`'s shape, with a "Hide" button added.
- **F2.5 Lightbox Viewer** — depends on F2.3 for `MediaActions`. The
  lightbox replaces MediaDetail; it mounts `MediaActions` in its own
  header. F2.5 also extends the action cluster with F2.4's Hide button
  if F2.4 has shipped first; the cluster is composable.

## §18 Risks and mitigations

| Risk                                                          | Mitigation                                                                                       |
|---------------------------------------------------------------|--------------------------------------------------------------------------------------------------|
| Selection ∩ membership ambiguity on /albums/:id              | §9.7 specifies the intersection rule; `AlbumDetailStore.hasInAlbum` is O(1).                    |
| Polling on /shares wastes battery on long sessions            | Polls only while pending/revoking exists; auto-stops; 5s interval, no exponential.              |
| Bulk operations time out on huge selections                   | Add chunks at 500/req with concurrency 4; remove with concurrency 4; partial-failure surfacing. |
| Share creation succeeds but broker offline → row sticks failed | Failed rows surface Retry **and** Revoke (cleanup path).                                        |
| Stale `share.Scope.broker_status` after server-side TTL expiry | UI overrides label as "Expired" when `expires_at < now`; Revoke remains available.              |
| User confused by `hub:user_id` grantee format                 | Helper text under input; v1 users are CLI-aware.                                                |
| Album-detail sort surprise from new `taken` mode             | Default to "Date taken" + sort dropdown shows current mode; switch persists to URL only via reload (no localStorage v1). |
| 503 on share Create misleading the user about the broker      | Copy says "Sharing is unavailable" — no broker mention. Broker-down rows live in pending/failed. |
| ActionBar appearing on routes where it shouldn't             | Each route renders ActionBar itself; no global mount. /albums and /shares simply don't render it. |

## §19 Implementation outline (preview only — full plan in writing-plans)

Approximate task slices, in dependency order:

1. Backend: `AlbumMediaFilter.SortBy = "taken"` with `NULLS LAST`.
2. Backend: `scopeDTO.target_summary` + service helper.
3. Backend: `listSharesOutput.next_offset`.
4. `make api-generate` + commit regenerated types.
5. Frontend foundations: `SelectionStore` extensions, `MonthChunk`
   `headerAction` slot, `VirtualGrid` `timelineChrome` prop,
   `GroupSelectButton`, route-aware ActionBar mounting.
6. Sidebar grouping (BROWSE / CURATE / MANAGE).
7. `AlbumsStore` + `/albums` route + cover grid + new-album modal.
8. `AlbumDetailStore` + `/albums/:id` route + header + sort + flat
   VirtualGrid.
9. `MediaActions` component + AddToAlbumModal + ShareModal scaffolding.
10. AddToAlbumModal full behavior (search, two-step, create-new,
    chunked add, error mapping).
11. ShareModal full behavior (validation, parsing, error banner).
12. `SharesStore` + `/shares` route + table + state pills + actions.
13. Drawer + lazy detail/preview.
14. Polling.
15. `?album_id` query filter wiring + 409 album-delete toast deep link.
16. Bulk-remove on album detail.
17. e2e fixtures + playwright tests.
18. roborev sweep + final verification.

The implementation plan (writing-plans) breaks these into bite-sized
TDD tasks with explicit code, expected output, and commit boundaries.
