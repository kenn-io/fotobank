# Which files can I use?

Importing a file preserves its exact bytes. Showing a thumbnail and playing a
video are separate capabilities. Keep source copies while trying this pre-alpha.

| Files | What to expect |
| --- | --- |
| JPEG, PNG, GIF, WebP | Stored originals and previews for supported inputs. GIF previews use the first frame, not an animation. |
| JPEG with an embedded ICC color profile | Stored original, but the current preview producer rejects the profile, including tagged sRGB. Do not strip metadata from your originals to work around this. |
| ARW, RAF, DNG, CR2, NEF | Previews depend on a usable embedded JPEG. Fotobank does not develop the RAW sensor data. |
| HEIC (`.heic`) | No Fotobank thumbnail producer. Import support does not imply that your browser can display the original. |
| MP4, MOV, M4V, AVI, MPG, MP2 | Stored originals and byte-range serving for seeking. No generated video thumbnails; playback depends on the browser's container and codec support. |
| XMP | Attached metadata file, not a standalone photo. An orphaned sidecar is rejected. |

Malformed files and unsupported color profiles can prevent preview generation
even for a listed image format. `media show <id> --json` reports thumbnail state;
the original remains available through `media download` when import completed.
Repeated thumbnail regeneration does not add a missing decoder.
Other extensions, including `.heif` and camera RAW formats not listed above,
are not discovered by the current importer.

## How are related files grouped?

A JPEG and a camera RAW with the same directory and filename stem become one
photo. The JPEG is the primary file; the RAW is its camera source. A matching
XMP sidecar attaches to the RAW when present, otherwise to the primary.
Ambiguous groups are reported rather than guessed. See [import](import.md).

## What changes when I edit a file?

An ordinary download is a separate copy. Editing it does not update Fotobank.
A [checkout](checkouts.md) tracks working files and lets you explicitly commit
edits as new versions. Current writeback does not import new files, infer
renames, apply deletions, or resolve conflicts. Checkouts also require filenames
that are portable to Windows; originals with incompatible names can remain
stored even when checkout creation rejects them.
