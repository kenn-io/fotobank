# Set up Fotobank

Fotobank needs three separate storage locations:

- a local state directory for SQLite and disposable cache data;
- a durable Docbank vault for original media and immutable versions; and
- an artifact directory for rebuildable thumbnails.

The Docbank and artifact directories may be siblings on the same mounted
filesystem, but neither may contain the other.

Generated thumbnails are written to the artifact directory. By default they
are also cached beneath the local state directory; set
`thumbs.cache_enabled = false` when a second local copy is not useful.

Backups use a separately initialized repository and are disabled by default.
See [Back up and restore](backup.md) to enable scheduled complete archives.

## Create the configuration

```sh
fotobank config init
fotobank config path
```

`config init` writes the complete example to the normal configuration path. It
creates missing parent directories and never replaces an existing file. Use an
explicit location when a service manager or automation owns configuration:

```sh
# After creating the fotobank service account:
sudo install -d -o fotobank -g fotobank -m 0700 /var/lib/fotobank-control
sudo -u fotobank fotobank config init --config /var/lib/fotobank-control/config.toml
```

The daemon writes its runtime record, logs, and locks beside the configuration.
That directory must be writable by its OS account. Keep it on local storage,
separate from the photo storage roots below, so recovery can start when those
roots are unavailable. Run lifecycle and application commands under the same
account and with the same configuration.

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

Validation resolves storage paths and rejects overlap between the Docbank,
artifact, and cache directories. It does not verify an external mount: the
server reports an absent NAS through readiness, and commands that need it fail.

For a read-only check of the complete local setup, run:

```sh
fotobank config diagnose
```

The diagnostic reports configuration, SQLite, Docbank, NAS artifacts,
checkout boundaries, identity, and backups separately. It names the setting
and corrective action for each failure. It does not create a database, vault,
storage root, or backup directory, and it never writes a probe file.
Add `--json` for scripts and agents. It emits the same checks as structured
results, including failures, and exits nonzero if any check reports an error.

Start the application after validation:

```sh
fotobank daemon start
```

The command starts Fotobank in the background and prints the web UI URL.
The default is `http://127.0.0.1:8090`. Configure the listeners in `config.toml`:

```toml
[http]
listen_address = "127.0.0.1:8090"
base_url = "" # optional browser-facing URL when using a reverse proxy

[daemon]
listen_address = "127.0.0.1:0" # local control port; 0 selects an available port
start_timeout = "1m"
stop_timeout = "2m"

[observability]
admin_listen = "127.0.0.1:9090" # readiness and metrics
```

The web listener can use a fixed port or port `0`; startup prints the actual
bound URL unless `http.base_url` is set. The control listener must use a numeric
loopback address. It is separate from the web UI and photo API.

```sh
fotobank daemon status --json
fotobank daemon restart
fotobank daemon stop
```

Repeated `start` calls reuse the running daemon. Restart applies configuration
changes and prints the new web UI URL. Status and stop never start a daemon.
Stop waits for requests, workers, and storage to close; it reports a timeout
instead of force-killing unfinished writes. Background output goes to
`<config>.operator/daemon.log` and is replaced on the next launch. Startup
errors include that path and recent output.

Use `fotobank daemon run` or `fotobank serve` for foreground operation under a
supervisor or while developing. Lifecycle commands use the same configuration
selection as the rest of Fotobank (`--config` or `FOTOBANK_CONFIG`).

See the [automation guide](automation.md#know-which-process-owns-the-vault) for
which commands start the daemon and which require an already-running daemon.
