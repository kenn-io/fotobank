# Frontend

The web app lets people browse and manage the library through the daemon's API.
This page describes its build, server contract, and shared interaction behavior.

## Build and embedding

`frontend/` is a Svelte and TypeScript single-page application built with Bun.
The production build is copied into `internal/web/dist` and embedded in the Go
binary. `internal/web` serves the application shell for non-API paths so client
routes work after a direct browser refresh.

`make build` builds the frontend before compiling Go. A direct `go build` uses
whatever assets are already present in `internal/web/dist` and is therefore not
the normal full-product build.

## API contract

The frontend calls `/api/v1`. Huma generates `openapi.yaml` through the same
JSON operation definitions used by the server, including search, AI, and
facets. The generator supplies no runtime services and does not open storage
or contact providers. Handlers check service availability when called.

`make api-generate` updates the OpenAPI file and the Orval TypeScript client.
Browser calls use its named operations. The shared fetch transport preserves
HTTP error results, cancellation, and keepalive requests, and sends repeated
query values as separate parameters. JSON API changes must regenerate the
contract and clients before commit. The live server exposes
the schema at `/api/openapi.json` and interactive documentation at `/api/docs`.

Full-size media, thumbnails, and events also appear in the generated contract.
Orval's fetch generator emits `generated/browser.ts` for these streaming routes.
Images, native download links, and EventSource use its typed URL builders, so
browser code does not construct API paths. The browser still owns image loading,
download streaming, and event reconnection; the raw Go handlers retain their
existing cache, range, and streaming behavior.

## Routes and state

`frontend/src/App.svelte` owns top-level routing and session feature flags.
Route components cover the library, media detail, map, albums, hidden library,
shares, search, sessions, and user/admin AI settings.

The browser treats server data as authoritative. Local state holds view
preferences, paging cursors, lightbox position, and short-lived optimistic UI
only. Server-sent events invalidate affected views; reconnect or missed events
fall back to ordinary refetches.

Hidden-media unlock state is established by an HTTP-only cookie. Frontend code
does not store the passcode or reproduce authorization decisions. A hidden
route still expects the server to reject an expired or missing unlock.

Sharing controls are shown only when `/me` reports the feature enabled. This is
presentation policy; backend services remain authoritative for permissions.

The shared desktop and mobile navigation links to `/settings`. That page routes
users to existing AI controls, Hidden photos, and workflow guides. It does not
edit host configuration or grant additional permissions. AI provider settings
remain behind the existing admin checks on their own route.

AI settings reports acknowledgement, queue, retry, and inspection-save results
on the page. Failed actions stay retryable; an inspection preference becomes
local state only after the daemon accepts it. Status and recent-failure reads
have separate retry messages, so an unavailable list is not shown as an empty
one. The shared health store marks failed refreshes as unavailable; the shell
does not keep reporting an old healthy result during that outage.

## Feedback and dialogs

The Shares list uses a button on each share name to open the existing details
drawer. Opening it moves keyboard focus to Close; closing returns focus to the
share name. At phone widths, table fields stack with labels so details and
Revoke/Retry stay available without horizontal scrolling.

`ConfirmModal` uses Kit's focus attachment to move keyboard focus into the
dialog, contain Tab navigation, and restore focus on dismissal. Fotobank's
modal stack still owns Escape handling. While confirmation is pending, focus
stays on the dialog and dismissal remains disabled. This applies to album
deletion and share revocation. A share drawer stays open behind its revocation
confirmation so canceling can return focus to Revoke; Escape closes only the
confirmation while it is open. Other custom dialogs have separate lifecycles.

Album removal reports server and connection failures through the shared
notification stack. Each photo has its own result: successful removals update
the album and clear that selection; failed photos remain selected for retry.
Failed album deletion keeps the album open and shows a notification. Active
CLI shares still block deletion with instructions for revoking those shares.

Library, Sessions, and Search distinguish failed photo reads from successful
empty results. Their stores retain the request and any loaded pages; an inline
Retry action repeats the failed page without clearing filters or the query.
Automatic pagination pauses on failure. A new search or filter selection clears
the old error, and stale requests cannot replace the current request's state.
Search hides indexing status while a request is loading or failed rather than
presenting missing response data as zero indexing progress.

After a successful unfiltered read returns no photos, Library and Sessions show
`EmptyLibrary` import guidance. It tells users to run the import command on the
daemon's machine with the same account and configuration, explains that imports
leave source files unchanged, and links to setup and import guides. Users of
someone else's library are directed to its operator. Sessions resets retained
Library filters before loading. Filtered empty results and request failures
keep their separate messages.

## Media presentation

Library, Sessions, Albums, and Hidden offer Kit checkboxes for touch and
keyboard selection. The checkbox updates the shared selection without opening
the photo; the photo link still opens detail or the lightbox. These routes also
support Ctrl/Cmd-click and Shift-click selection. Selection actions use Kit
buttons and wrap on narrow screens, with larger touch targets.

Grid selection is opt-in and requires a route with selection actions. Search
and Map are browse-only: they show no selection controls or outlines, leave
Ctrl/Cmd-click and Shift-click to the browser, and open the viewer on their
current results without using another route's selection.

Library and search results use thumbnail versions as cache-busting input.
Media detail and lightbox views request full-size media only when needed.
Videos use HTTP byte ranges so browsers can seek without downloading the whole
file.

RAW files normally display a generated JPEG preview. Camera source files and
XMP sidecars are product relationships, not independent navigation identities
in the asset model.

A single-file asset has no attachment rows, but its primary original remains
available from the detail page.

## Shared controls and layout

Shared control behavior and accessibility come from the pinned
`@kenn-io/kit-ui` source dependency. Fotobank imports the library's theme
tokens before `frontend/src/app.css`; the app stylesheet then maps those tokens
to Fotobank's dense, amber-accented darkroom palette, IBM Plex UI type, and
Fraunces display type. Existing components and new shared controls use the same
token vocabulary, so adopting a shared control does not imply adopting another
product's visual identity.

Helper text, counts, and placeholders use the readable `--text-muted` color.
The darker `--fb-text-faint` color is for decoration, such as dotted leaders,
not text. Browser tests check representative text contrast on desktop and phone.

At 760px and below, `ThreeColumnLayout` gives the main view the full width and
puts the existing sidebar behind a “Browse & filters” button. The panel
replaces the content view while open rather than covering it with a modal.
Changing sections closes the panel; changing filters keeps it open until
the user closes it or presses Escape. The sidebar remains mounted, preserving
its filter state. Desktop retains its persistent sidebar and timeline rail;
on phones, year shortcuts flow above the grid instead of reserving a rail.
The header puts search on its own row, and search options wrap on narrow views.
The map fills the main pane's available height rather than subtracting a
separate header estimate from the viewport.

Album creation uses the shared modal, text field, buttons, and empty state.
The controls inherit Fotobank's darkroom palette, and the modal supplies the
close, backdrop, focus-trap, and keyboard behavior for the route.

The app header composes kit-ui's search field with Fotobank's navigation
behavior. The shared control owns the search icon, shortcut badge, and clear
action; `SearchBar.svelte` owns query synchronization, the global keyboard
shortcut, and trimmed submission to the router. Search sorting and media-type
filters use shared segmented controls, while the hidden-media option uses the
shared checkbox; the search route remains the owner of query and filter state.
Search opens with its filter panel collapsed. The Filters button reveals an
inline panel, while sorting and removable active filters stay visible outside
it. Closing the panel preserves its inputs; selected filters remain in the URL
and survive reloads. Phone layouts stack field groups and enlarge touch targets.

`make frontend-check` runs `kit-ui-check` in warning mode alongside type checks
and unit tests. Warnings identify remaining local control equivalents without
blocking incremental adoption. Architecture docs record interaction and data
boundaries, not old mockups or dated aesthetic proposals. A UI pull request
includes a screenshot of the implemented result using synthetic data.

## Tests

- `make frontend-check` installs locked dependencies and runs ESLint, the
  advisory kit-ui checker, Svelte/TypeScript checks, and frontend unit tests.
- The CI web-application job runs those checks and `make frontend`, building
  production assets from source and copying them into the Go embed directory.
  Its Node pin matches `mise.toml`; Bun reads its pin from
  `frontend/package.json`. Like the other Linux jobs, it runs on GitHub-hosted
  `ubuntu-latest` runners.
- `frontend/eslint.config.js` applies recommended JavaScript, TypeScript, and
  Svelte rules to hand-written code, including rune modules. Generated API
  types and build/test output are excluded from lint (generated types still
  participate in type checking). Documented exceptions allow non-reactive
  collections, text-only unkeyed lists, and permissive test payloads; lint does
  not require unrelated component refactoring.
- Route tests use synthetic API data and exercise state and accessibility
  behavior.
- Playwright tests run against `cmd/e2e-server`, which builds a self-contained
  temporary Fotobank environment.
- CI runs the ordinary Chromium workflow suite and the sharing-disabled suite
  against a freshly built application. This job uses a disposable GitHub-hosted
  Linux runner to install browser system dependencies. Failed tests retain
  screenshots and traces for seven days; they contain synthetic fixture data.
  Run them locally with `bun run test:e2e` and
  `bun run test:e2e:sharing-disabled` from `frontend` after installing Chromium
  with `bunx playwright install chromium`.
- Scale fixtures are versioned and cached outside the repository. Bumping the
  seed version invalidates the cache when fixture semantics change. Scale tests
  are excluded from the ordinary suite; run `bun run test:e2e:scale` explicitly.

Frontend tests do not call a developer's running Fotobank instance or reuse a
real library.
