# fotobank

A self-hosted photo archive tool: deduplicates on import, gives files consistent date-based names, maintains a SQLite metadata registry, and (in progress) serves thumbnails and albums over a small HTTP API.

Your photos stay on your storage. Fotobank coexists with Lightroom Classic by organizing the files on disk; Lightroom can watch the same directory.

## Status

Early development. The CLI imports, reconciles, and generates thumbnails. The HTTP API serves media metadata, originals, and thumbnails. Albums are in design. Sharing is deferred.

## Build

Requires Go 1.26+. Pure Go, no CGO.

```shell
make build          # → bin/fotobank
make install        # copies bin/fotobank to ~/.local/bin or $GOBIN
```

Or directly:

```shell
go build -o bin/fotobank ./cmd/fotobank
```

## Configuration

Copy `testdata/config.example.yaml` (if present) or write one by hand. The loader looks for `--config <path>` first, then `FOTOBANK_CONFIG`, then `$XDG_CONFIG_HOME/fotobank/config.yaml`. Minimum config:

```yaml
identity:
  mode: stub
  stub:
    hub: local
    user_id: me

flash:
  root: /srv/fotobank/flash

storage:
  nas_root: /srv/fotobank/archive
  thumbs_cache_enabled: true
```

## Commands

```shell
fotobank server                          # HTTP API + background workers
fotobank import /path/to/source          # import photos/videos into the archive
fotobank reconcile                       # reconcile on-disk files with the DB
fotobank thumbs regenerate --all         # enqueue rows for thumbnail rebuild
```

## Architecture

See [`CLAUDE.md`](CLAUDE.md) for the package layout and layering rules.

Design docs are under `docs/superpowers/specs/`, implementation plans under `docs/superpowers/plans/`.
