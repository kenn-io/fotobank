# CLAUDE.md — fotobank

Personal photo management. A self-hosted alternative to cloud photo services: deduplication, consistent file naming, a metadata registry, and (in progress) thumbnails, albums, and sharing.

This is a **Go project**. An earlier Python prototype was deleted at 2026-04-23; don't look for `.py` files.

## Quick reference

```bash
make build            # debug binary → bin/fotobank (requires a C compiler; CGO)
make build-release    # release binary
make install          # copy to ~/.local/bin or $GOBIN
make dev              # live-reload via air (runs `server`)
make test             # go test ./... -shuffle=on
make test-short       # short tests only
make lint             # golangci-lint --fix + testify-helper-check
make nilaway          # pre-push tier
make tidy             # go mod tidy
make api-generate     # regenerate openapi.json
make install-hooks    # install prek git hooks
```

## Layout

```
cmd/
├── fotobank/              — CLI entry; main wires into internal/cli
└── fotobank-openapi/      — generates openapi.json from the huma API

internal/
├── album/                 — albums domain (Plan D; in design)
├── broker/                — sharing broker stub (Plan E)
├── cli/                   — cobra subcommands (server, import, thumbs, …)
├── config/                — YAML config loader + defaults
├── db/                    — sqlx wrapper + migrations
│   └── migrations/        — golang-migrate SQL files (up/down pairs)
├── errs/                  — cross-cutting sentinel errors
├── exifread/              — pure-Go EXIF reader
├── httpapi/               — huma/v2 REST API
├── identity/              — stub-mode identity (phase 1)
├── ingest/                — import pipeline
├── media/                 — media domain + repo
├── migrate/               — runs db/migrations at boot
├── owners/                — owner principal type
├── reconcile/             — on-disk ↔ DB reconciler
├── service/               — auth-scoped wrappers over repos
├── share/                 — scopes domain (Plan E)
├── storage/               — NAS + flash cache
├── testutil/              — shared test helpers
├── thumb/                 — thumbnail pipeline (Plan C)
└── version/               — build-metadata globals set from ldflags
```

## Layering

Three tiers per domain: **repo → service → transport**.

- **Repo** (`internal/<domain>/repo.go`) is DB-only. No auth, no identity plumbing. Takes IDs, returns rows.
- **Service** (`internal/service/*.go`) is the auth boundary. Every exported method takes `caller owners.Principal` and either scopes queries to that principal or returns `errs.ErrNotFound`.
- **Transport** is either `internal/httpapi/` (huma routes, mounted on `http.ServeMux`) or `internal/cli/` (cobra subcommands). Both go through service — CLI must not call repos directly because the repos don't enforce ownership.

Background workers (e.g. `internal/thumb/worker.go`) follow the same rule: they're driven from a queue populated via repo/service calls, not by fan-out from a transport.

## Conventions

- Errors: sentinels in `internal/errs/errs.go` (ErrNotFound, ErrOwnerMismatch, ErrPermissionDenied, ErrAlreadyExists, ErrInvalidArgument, ErrConcurrentImport, ErrIdentityMissing, ErrBrokerUnavailable, ErrDirectAccessBlocked). HTTP mapping in `internal/httpapi/errors.go::Translate`. Wrap with `fmt.Errorf("doing X: %w", err)`.
- Tests use `testify/require`. `testutil.OpenTestDB(t)` spins a fresh migrated SQLite DB per test.
- Migrations: pre-alpha policy — there is one migration, `000001_initial_schema.{up,down}.sql`, and you edit it directly for any schema change. No new numbered migrations until fotobank ships to real users. Both files (up + down) move together on every change.
- HTTP: JSON routes use huma. Byte-streaming routes (`/original`, `/thumb`) use raw `http.HandlerFunc` on the same mux.
- Identity: phase 1 is stub mode — one principal per config, set via `identity.mode = "stub"`. Other modes are rejected by the CLI tooling that mutates DB state.
- Runtime: SQLite via `mattn/go-sqlite3` with the `sqlite-vec` auto-extension. CGO is enabled. One driver registration across the app — see `internal/db/sqlitevec.go`.
- Cross-build (e.g. macOS host → Linux deploy): set `CC` to a cross-compiler (`zig cc -target x86_64-linux-musl`, `musl-cross`, or equivalent). Plain `GOOS=linux GOARCH=amd64 go build` without a cross `CC` will fail at link time.

## Plans

Design docs live in `docs/superpowers/specs/`, plans in `docs/superpowers/plans/`.

- **Plan A** — DB foundation, migrations, principals, storage. **Done.**
- **Plan B** — Import pipeline, reconcile, media HTTP CRUD. **Done.**
- **Plan C** — Thumbnail pipeline (queue, worker, flash cache subdir, `/thumb` endpoint). **Done.**
- **Plan D** — Albums (CRUD service + HTTP + CLI; no sharing). **Done.**
- **Plan E** — Sharing: scopes, broker registration, outbox worker, cross-owner reads. **Done (owner side; grantee-side viewing deferred).**
- **Observability** — Structured logging (slog), Prometheus metrics, /readyz, admin HTTP listener. **Done.**

## Instructions for agents

- Commit directly to master. No feature branches, no worktrees.
- **Commit every turn** — always commit your work at the end of each turn, no exceptions. Don't ask first.
- Use conventional commit messages (`fix:`, `feat:`, `refactor:`, `docs:`, `test:`, `chore:`, optionally scoped like `fix(httpapi):`). Imperative mood, ≤72 char subject.
- One logical change per commit. Split unrelated changes into separate commits.
- Never amend commits — always create a new commit for fixes.
- Never bypass pre-commit hooks (no `--no-verify`). If a hook fails, fix the underlying issue and create a new commit.
- Never push or pull unless explicitly asked.
- Schema changes go in-place into `000001_initial_schema.{up,down}.sql` (pre-alpha policy). Keep up and down in sync.
- When touching HTTP routes, run `make api-generate` (the prek hook does this automatically on commit).
- Prefer `require.ErrorIs` for sentinel checks; raw `==` comparison misses wrapped errors.
- The existing `httpapi.Translate` maps `errs.ErrOwnerMismatch → 403`, which matches the scopes/sharing surface but not the albums surface. If a surface needs a different mapping, write a local translator that overrides the sentinels it cares about and delegates the rest to `Translate`.
