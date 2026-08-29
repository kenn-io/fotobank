# Frontend

## Build and embedding

`frontend/` is a Svelte and TypeScript single-page application built with Bun.
The production build is copied into `internal/web/dist` and embedded in the Go
binary. `internal/web` serves the application shell for non-API paths so client
routes work after a direct browser refresh.

`make build` builds the frontend before compiling Go. A direct `go build` uses
whatever assets are already present in `internal/web/dist` and is therefore not
the normal full-product build.

## API contract

The frontend calls `/api/v1`. Huma generates `openapi.json` through the same
route builder used by the server, but the generator supplies no runtime
services. Registrations that require search, AI, or facets services therefore
do not appear in the checked-in schema even though a configured server exposes
them. Raw byte and event routes are also outside Huma's JSON schema. The
checked-in OpenAPI file is a generated subset of the production API, not a
complete route inventory.

`make api-generate` updates the OpenAPI file and generated TypeScript schema.
HTTP changes covered by that schema must regenerate both before commit.

Full-size media and thumbnail endpoints are raw byte routes because they need
range requests, streaming, cache validators, and content headers. JSON routes
remain in the generated contract.

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

## Media presentation

Library and search results use thumbnail versions as cache-busting input.
Media detail and lightbox views request full-size media only when needed.
Videos use HTTP byte ranges so browsers can seek without downloading the whole
file.

RAW files normally display a generated JPEG preview. Camera source files and
XMP sidecars are product relationships, not independent navigation identities
in the asset model.

![Media detail showing one asset with its JPEG primary and DNG original](images/asset-files.png)

A single-file asset has no attachment rows, but its primary original remains
available from the detail page.

![Media detail showing the original download for a single-file asset](images/single-file-download.png)

Shared control behavior and accessibility come from the pinned
`@kenn-io/kit-ui` source dependency. Fotobank imports the library's theme
tokens before `frontend/src/app.css`; the app stylesheet then maps those tokens
to Fotobank's dense, amber-accented darkroom palette, IBM Plex UI type, and
Fraunces display type. Existing components and new shared controls use the same
token vocabulary, so adopting a shared control does not imply adopting another
product's visual identity.

![Fotobank header using the darkroom theme over kit-ui tokens](images/kit-ui-theme-foundation.png)

`make frontend-check` runs `kit-ui-check` in warning mode alongside type checks
and unit tests. Warnings identify remaining local control equivalents without
blocking incremental adoption. Architecture docs record interaction and data
boundaries, not old mockups or dated aesthetic proposals. A UI pull request
includes a screenshot of the implemented result using synthetic data.

## Tests

- `make frontend-check` runs type checking and frontend unit tests.
- Route tests use synthetic API data and exercise state and accessibility
  behavior.
- Playwright tests run against `cmd/e2e-server`, which builds a self-contained
  temporary Fotobank environment.
- Scale fixtures are versioned and cached outside the repository. Bumping the
  seed version invalidates the cache when fixture semantics change.

Frontend tests do not call a developer's running Fotobank instance or reuse a
real library.
