# Search and AI

Fotobank searches photo metadata and can use optional AI to describe and find
images. AI is disabled by default. This page describes the current indexes,
processing jobs, and privacy limits.

## Search indexes

Fotobank combines two SQLite-backed indexes:

- FTS5, SQLite's full-text index, stores searchable captions, tags, camera/lens
  values, filenames, and location labels.
- sqlite-vec stores image embeddings: numeric descriptions used to compare
  images and search text.

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

Manual backfill and retry-failed requests use the same daemon service through
HTTP and the CLI. Both require owner consent and scope queued work to that
owner. Embedding tasks resolve the building generation from one live settings
snapshot and use generation-specific gaps; `--force` does not discard existing
vectors. Retry captures a failure cutoff before its batch loop so fresh worker
failures are not repeatedly retried by the same request. Queueing remains
available while AI processing is globally paused.

Embedding backfill and retry are restricted to stub identity mode at the
service boundary. Generation activation and embedding events use the configured
stub owner; header-mode requests are rejected before generation or queue writes.
Tag and caption requests remain owner-scoped in either identity mode.

Generation administration uses the daemon's local operator API in stub mode.
Listing uses the activator's visible-media eligibility counter. Promotion first
provides generation details and the server's retention window for the CLI's
warning and confirmation; the write transaction then requires the target to
still be retired. Declining the prompt sends no promotion request. The daemon
may start for this read-only inspection before the prompt appears.

Compaction dry-run and the scheduled/manual sweep share the same candidate
query on the read-only pool. Each deletion rechecks retirement and age in its
write transaction. A failed dry-run prints no success summary. A partial
failure returns the number already dropped and an error; the CLI reports both
and exits nonzero. Generation administration does not require AI provider
availability or processing consent and does not itself enqueue provider work.

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

The current implementation keeps vectors, prompts, job queues, and search
lifecycle in Fotobank. Source projections use Fotobank media/asset identity and
exact input versions. Docbank already supplies source extraction, including
camera evidence, and canonical image previews through `internal/content`.

## Future Docbank integration

The accepted boundary places reusable model outputs, embeddings, and retrieval
in Docbank. Fotobank will consume those results and apply photographer-facing
curation, ownership, visibility, and query behavior. That integration is not
implemented yet; the packages above describe the code that runs today.

## Failure and privacy rules

- Gateway probes are bounded and never make health endpoints enqueue work.
- AI status requests check providers. Daemon startup and consent recording do not.
- Provider checks use current settings, including changed endpoints and credentials.
  With AI and embeddings enabled, `embed.provider` reports the embedding
  endpoint's image/text check result.
- The daemon caches successful and failed embedding checks for 30 seconds.
  It allows one check at a time, with a five-second total timeout independent
  of the initiating caller's cancellation. Endpoint, credential, model,
  dimension, or timeout changes invalidate the cached result.
- `last_check_at` records the actual check time. Reading the cache does not
  change it. Owner consent and queue counts are read fresh.
- Provider checks use synthetic inputs and never read photos.
- Provider outages do not prevent diagnostics or consent recording. Local
  validation still rejects invalid URLs, missing models, and invalid dimensions.
- Embedding health errors expose fixed categories, not provider response bodies
  or arbitrary error text. The same response serves photo users and operators;
  detailed provider errors are not copied into diagnostic logs by the probe.
- Logs include opaque media/job identifiers and failure categories, not image
  bytes, prompts containing private content, or local source paths.
- Disabled or unreachable AI does not prevent ordinary import, browsing,
  albums, shares, or metadata search.
- Search, autocomplete, facets, and completeness calculations exclude hidden
  media unless a valid owner unlock explicitly includes it.
- Gap scans exclude hidden media by default. Import queues AI work while a new
  media row is visible, and queued jobs do not uniformly re-check `hidden_at`
  before sending a preview to the configured provider. Tag and caption workers
  and embedding workers require the owner's recorded hidden-processing
  acknowledgement before resolving previews or calling providers. Unacknowledged
  jobs are blocked with `acknowledgement_required` and become eligible again
  after that owner records consent. Starting the daemon does not grant consent.
  Media hidden after it is queued can still be processed with that consent;
  hiding is not a queue-cancellation boundary in the active system.
