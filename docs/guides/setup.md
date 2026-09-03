# Set up Fotobank

Fotobank needs three separate storage locations:

- a local state directory for SQLite and disposable cache data;
- a durable Docbank vault for original media and immutable versions; and
- a durable artifact directory for thumbnails and Fotobank metadata snapshots.

The Docbank and artifact directories may be siblings on the same mounted
filesystem, but neither may contain the other.

Generated thumbnails are written to the artifact directory. By default they
are also cached beneath the local state directory; set
`thumbs.cache_enabled = false` when a second local copy is not useful.

## Create the configuration

```sh
fotobank config init
fotobank config path
```

`config init` writes the complete example to the normal configuration path. It
creates missing parent directories and never replaces an existing file. Use an
explicit location when a service manager or automation owns configuration:

```sh
fotobank config init --config /etc/fotobank/config.toml
```

Edit at least these values:

```toml
[flash]
root = "/var/lib/fotobank"

[docbank]
root = "/srv/photo-archive/docbank"

[nas]
root = "/srv/photo-archive/fotobank-artifacts"
```

The default `stub` identity is suitable for one local owner. Header identity
mode is for a deployment behind a trusted identity-aware proxy; see
[Runtime and boundaries](../architecture/runtime.md#identity-and-authorization).

## Validate before writing data

```sh
fotobank config validate
```

Validation resolves storage paths and rejects overlapping authority, artifact,
and cache roots. It does not make an unavailable external mount safe: the
server reports an absent NAS through readiness, and commands that need it fail.

For a read-only check of the complete local setup, run:

```sh
fotobank config diagnose
```

The diagnostic reports configuration, SQLite, Docbank, NAS artifacts,
checkout boundaries, identity, and backups separately. It names the setting
and corrective action for each failure. It does not create a database, vault,
storage root, or backup directory, and it never writes a probe file.

Start the application after validation:

```sh
fotobank serve
```

The default web address is `http://127.0.0.1:8090`.
