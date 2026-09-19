# Set up Fotobank for someone else

Use this procedure when an agent or administrator prepares a person's first
library. Fotobank is pre-alpha. Start with copies of a small collection, keep
independent originals, and do not promise that a successful trial makes it safe
to discard them.

## Agree on the setup

Ask the person to choose:

- The machine and OS account that will run Fotobank. Application commands run
  there, under that account; an import path is not a remote upload.
- Separate folders for the catalog, original files, and thumbnails. A local
  disk is enough for a trial. Confirm available space: imports copy files,
  checkouts make further copies, and backups need separate capacity.
- A source folder containing sample copies and a separate backup destination.
- Who may access the web app. Start with loopback access. Do not expose the
  default single-user identity to a network as though it were a login system.
- Whether any photos may be sent to an AI provider. Leave AI disabled unless
  the person explicitly approves the provider and processing policy.

Do not choose a storage path simply because a missing mount left an empty
directory. Do not change repository visibility, publish a share, enable external
processing, or delete source files as part of setup.

## Install and initialize

Follow [setup](setup.md) for the supported installation path and folder creation.
Use one explicit configuration selection throughout the session:

```sh
export FOTOBANK_CONFIG="$HOME/.config/fotobank-trial/config.toml"
fotobank config init
```

These are POSIX-shell examples. In PowerShell, set the same environment variable
with `$env:FOTOBANK_CONFIG = 'C:\path\to\config.toml'`.
If a configuration already exists, inspect it rather than replacing it.
Use absolute storage paths so a different working directory cannot select a
different library. Keep configuration on available local storage outside the
photo folders; its directory must be writable for daemon runtime files.

Edit the three storage roots as described in setup, then inspect them:

```sh
fotobank config validate
fotobank config diagnose --json
```

Before the first start, missing catalog and uninitialized vault reports are
expected. Missing storage directories or an unavailable mount are not. For an
existing library, stop and investigate missing data instead of initializing a
replacement.

## Prove the first import

```sh
fotobank daemon start
fotobank config diagnose --json
fotobank import /path/to/sample-copies --json
fotobank media list --limit 5 --json
```

Record the web URL printed by startup. Check exit codes and the import result's
failures, not just whether some photos appeared. Open the web app and inspect
several photos. A missing thumbnail may be a [format limitation](formats.md),
not a missing original.

Select an ID from the returned items and verify an independent download:

```sh
fotobank media show <media-id> --json
fotobank media download <media-id> --output /existing-folder/check.jpg --json
```

Use a new destination filename and the appropriate extension. Compare the
returned SHA-256 with the input file's checksum. Check a related RAW or XMP
attachment too if the sample collection contains one. The download command
verifies bytes against the catalog; comparing with the source additionally
checks that you imported the intended file.

## Prove recovery before a larger import

Follow [backup and restore](backup.md): initialize a separate repository, create
and verify an archive, then restore into separate storage. Check an album and
downloaded originals in the recovered library. Do not delete the original
deployment to simulate a loss. Keep optional AI and scheduled backups disabled
in the recovered test copy.

Only enable scheduled backups after the person approves their destination and
retention count. Repositories are not encrypted. Configuration and provider
credentials need separate protection because archives do not contain them.

## Hand over an operable library

Give the person the web URL, installed version, OS account, configuration path,
storage paths, backup location and schedule, and the result of the recovery
drill. Do not include credentials in the handoff or logs. Include:

```sh
fotobank daemon status --json
fotobank daemon stop
fotobank daemon start
```

Explain whether a supervisor or Fotobank's own background launcher starts the
process. For a supervised installation, use the supervisor to stop or restart it.
Point to [automation](automation.md) for JSON results and retry rules,
[checkouts](checkouts.md) for editing, and [optional AI](ai.md) before enabling
providers. A running daemon is not proof that backups or queued thumbnails have
finished; verify those results separately.
