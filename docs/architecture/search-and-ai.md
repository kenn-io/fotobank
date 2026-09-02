# Search and AI

## Search indexes

Fotobank combines two SQLite-backed indexes:

- FTS5 stores searchable captions, tags, camera/lens values, filenames, and
  location labels.
- sqlite-vec stores image embeddings for semantic similarity.

Both indexes are rebuildable projections keyed by the product media ID.
Repositories update source metadata and FTS rows transactionally when a
product mutation would otherwise expose stale text.

`internal/search/hybrid` parses the query, applies owner and visibility
filters, retrieves lexical and vector candidates, normalizes scores, and
returns one ranked page. Cursor state records enough ordering information for
stable continuation. Hidden rows are filtered in SQL rather than removed after
ranking, which prevents result counts and timing from exposing them.

Autocomplete for tags and locations uses active visible data for the caller.
Facets apply the same camera, lens, tag, GPS, owner, and hidden filters as the
library query.

## AI input boundary

AI is optional and disabled by default. Vision and embedding calls use an
OpenAI-compatible gateway selected by effective runtime configuration.

The external input is a generated, downscaled JPEG preview. The encoder strips
metadata and bounds dimensions and byte size before a request leaves the
process. The AI system does not send a RAW file, source path, EXIF block, or
full-size media by default.

## Configuration and fingerprints

Effective AI configuration combines TOML defaults with database-backed admin
overrides. Validation occurs after the merge. Runtime providers publish an
immutable snapshot so workers see one coherent configuration per job.

Every generated result records a fingerprint derived from the task, provider,
model, prompt, and relevant input version. A result is active only when its
fingerprint matches the active configuration. This makes model or prompt
changes explicit instead of silently presenting stale output as current.

## Vision jobs

`internal/ai/jobs` is the durable work queue. Tagging and captioning jobs claim
rows with leases, run through bounded global and per-owner concurrency, parse
strict result shapes, and write active results transactionally. Failures retain
kind, message, attempt count, and retry timing. Unsupported inputs and operator
acknowledgements use explicit skipped state rather than fake success.

The gap scanner compares eligible visible media with active results, failures,
and skips, then queues only missing work. Retry commands can target a failed
set or one media item without bypassing ownership rules.

## Embedding generations

Embeddings use generations so a model or input change does not mix incompatible
vectors in one search space.

1. A building generation records its fingerprint and vector dimensions.
2. Workers create mappings from media IDs to vector row IDs and populate the
   sqlite-vec table.
3. Activation verifies completeness and dimensions, atomically makes the new
   generation active, and retires the previous one.
4. Retired generations remain for the configured retention window before
   compaction removes their mappings and vectors.

Only one generation is active for search. Thumbnail regeneration invalidates
the affected media's current mapping because the visual input changed.

Vectors remain Fotobank-owned while the Docbank content integration is proven.
They key source projections by Fotobank media/asset identity and exact input
versions. Docbank owns reusable source extraction, including camera evidence;
Fotobank owns the photographer-facing projection, prompts, and search
lifecycle.

## Failure and privacy rules

- Gateway probes are bounded and never make health endpoints enqueue work.
- Logs include opaque media/job identifiers and failure categories, not image
  bytes, prompts containing private content, or local source paths.
- Disabled or unreachable AI does not prevent ordinary import, browsing,
  albums, shares, or metadata search.
- Search, autocomplete, facets, and completeness calculations exclude hidden
  media unless a valid owner unlock explicitly includes it.
- Gap scans exclude hidden media by default. Import queues AI work while a new
  media row is visible, and queued jobs do not uniformly re-check `hidden_at`
  before sending a preview to the configured provider. Tag and caption workers
  require the owner's hidden-processing acknowledgement; the embedding worker
  currently does not. Media hidden after it is queued can therefore still be
  processed. Hiding is not a queue-cancellation boundary in the active system.
