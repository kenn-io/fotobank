# AGENTS.md — fotobank

Personal photo management. A self-hosted alternative to cloud photo services: deduplication, consistent file naming, a metadata registry, and (in progress) thumbnails, albums, and sharing.

This is a **Go project**. An earlier Python prototype was deleted at 2026-04-23; don't look for `.py` files.

## Quick reference

```bash
make build            # debug binary → bin/fotobank (requires a C compiler; CGO)
make build-release    # release binary
make install          # copy to ~/.local/bin or $GOBIN
make dev              # live-reload via air (runs `serve`)
make test             # go test ./... -shuffle=on
make test-short       # short tests only
make lint             # golangci-lint --fix + testify-helper-check
make nilaway          # pre-push tier
make tidy             # go mod tidy
make api-generate     # regenerate openapi.json
make docs-build       # build marketing site, guide, and Zensical docs
make docs-check       # build and validate the complete docs site
make docs-serve       # serve the generated site on localhost
make install-hooks    # install prek git hooks
```

## Layout

```
cmd/
├── fotobank/              — CLI entry; main wires into internal/cli
└── fotobank-openapi/      — generates openapi.json from the huma API

internal/
├── album/                 — albums domain
├── broker/                — sharing broker interface and stub
├── cli/                   — cobra subcommands (serve, import, thumbs, …)
├── config/                — TOML config loader + defaults
├── content/               — embedded Docbank boundary
├── contentresolver/       — asset/file/exact-version binding
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
├── service/               — auth-scoped wrappers over repos
├── share/                 — sharing scopes domain
├── storage/               — NAS + flash cache
├── testutil/              — shared test helpers
├── thumb/                 — thumbnail queue and worker
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
- Runtime: SQLite via `mattn/go-sqlite3` with the `sqlite-vec` auto-extension and the `sqlite_fts5` build tag. CGO is enabled. One driver registration across the app — see `internal/db/sqlitevec.go`. Direct `go build` / `go test` invocations need `-tags sqlite_fts5`; `make` targets pass it automatically.
- Cross-build (e.g. macOS host → Linux deploy): set `CC` to a cross-compiler (`zig cc -target x86_64-linux-musl`, `musl-cross`, or equivalent). Plain `GOOS=linux GOARCH=amd64 go build` without a cross `CC` will fail at link time.
- Scale fixtures (Playwright `test:e2e:scale`) are cached under `~/.cache/fotobank/scale-fixtures/` so repeat runs skip the seed. Bust the cache by bumping `mediaseed.SeedVersion`, setting `FOTOBANK_E2E_SCALE_NO_CACHE=1`, or `trash`-ing the cache dir. See `internal/testutil/scalecache/`.

## Architecture documentation

Living architecture documentation starts at
[`docs/architecture/README.md`](docs/architecture/README.md). It describes the
system that exists.

- Update the relevant architecture page in the same pull request that changes
  a system boundary, data flow, invariant, authority, or operational contract.
- Write for an agent or engineer entering the codebase now. Explain current
  ownership, behavior, failure handling, and the code entry points that enforce
  them.
- Remove or rewrite stale statements. Do not preserve a history of superseded
  designs inside architecture docs.
- Do not commit dated specs, implementation plans, milestone indexes, task
  checklists, brainstorming transcripts, or mockups. Temporary planning stays
  outside the repository.
- Track proposed and incomplete work in kata. Put review-specific reasoning in
  the pull request. Architecture docs may state an accepted boundary or
  current behavior, but they are not the implementation tracker.

## Task tracking

Outstanding work for fotobank is tracked in **kata** (`kata` CLI). The
workspace is bound to the Fotobank project through `.kata.toml`.

- Run `kata quickstart` at the start of a work session and follow its current
  command contract.
- Search before creating: `kata search "<phrase>" --agent`.
- Create durable work with an idempotency key:
  `kata create "<title>" --body "..." --label <label> --idempotency-key <stable-key> --agent`.
- Inspect with `kata show <ref> --agent`, `kata list --agent`, and
  `kata ready --agent`.
- Add dependency and parent relationships with `kata create` or `kata edit`
  flags such as `--blocked-by`, `--blocks`, and `--parent`. Cross-project refs
  use `<project>#<short-id>`, for example `docbank#abc4`.
- Close only verified work with `kata close <ref> --done --message "..."`
  and the required evidence flags. If work remains, comment and add
  `needs-review` instead.
- Never run `kata delete`, `kata purge`, or project removal commands unless the
  user explicitly asks for that exact destructive action and reference.

Don't track ad-hoc one-turn tasks in kata — it's for outstanding designed/planned work that survives across sessions. In-conversation step tracking belongs in TaskCreate/TaskList.

## Git and pull requests

1. Commit every turn that changes tracked files; never amend.
2. Never push to or commit on `main`. Use feature branches and pull requests.
   Use an isolated worktree when an implementation workflow requires one or
   when concurrent work must not share a checkout.
3. Push the feature branch and open or update its pull request as part of the
   handoff. Do not merge pull requests; merging is the user's job.
4. Run `prek run` before committing. Never bypass hooks with `--no-verify`; fix
   the underlying problem.
5. Use conventional commit messages (`fix:`, `feat:`, `refactor:`, `docs:`,
   `test:`, `chore:`, optionally scoped like `fix(httpapi):`). Use imperative
   mood and a subject of at most 72 characters. Keep one logical change per
   commit and split unrelated changes.
6. Write pull request descriptions in plain language for users and reviewers.
   Lead with the outcome and its user-understandable implications. Explain only
   the important boundary or tradeoff, and keep the description clear and
   concise.
7. Do not put routine test commands, validation checklists, implementation
   diaries, agent process, or unrelated debugging details in pull request
   descriptions. This includes `Evidence` sections and explanations of test
   runner tuning or pre-existing flakes. Report ordinary verification in the
   handoff instead. Include unusual evidence only when it materially changes a
   review decision or explains a product risk.
8. A pull request that changes the web interface must include a screenshot of
   the actual rendered result using synthetic data. Inspect it before
   publishing; never substitute a mockup for the implementation.
9. Do not poll, watch, repeatedly query, or wait for GitHub Actions unless the
   user explicitly asks you to monitor CI. Opening or updating a pull request
   does not grant that authority. Report any immediately available status and
   hand off without waiting for checks to finish.

## Instructions for agents

- Schema changes go in-place into `000001_initial_schema.{up,down}.sql`
  (pre-alpha policy). Keep up and down in sync.
- When touching HTTP routes, run `make api-generate` (the prek hook does this
  automatically on commit).
- Prefer `require.ErrorIs` for sentinel checks; raw `==` comparison misses
  wrapped errors.
- The existing `httpapi.Translate` maps `errs.ErrOwnerMismatch → 403`, which
  matches the scopes/sharing surface but not the albums surface. If a surface
  needs a different mapping, write a local translator that overrides the
  sentinels it cares about and delegates the rest to `Translate`.
