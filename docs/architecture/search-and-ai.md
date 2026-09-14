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

The daemon always provides metadata search. When AI or embeddings are
disabled, text queries use FTS5 without calling an
embedding provider, even if the catalog retains an active embedding generation.
Queries without searchable text use the catalog filters and date ordering.
Disabled embeddings are an ordinary metadata-search mode, not a provider-outage
banner.

Each text query obtains a client from the same current-settings snapshot used
by embedding workers. Admin changes to enablement, endpoint, and credentials
apply to the next query without a restart. An already-running query may finish
with its captured settings.

Queries still use the active generation's model and vector dimensions, even
when admin settings name a new model whose generation is still building. A
provider failure falls back to metadata search.

`internal/search/hybrid` applies owner and visibility filters in SQL before
ranking. Text search selects up to `search.k_per_signal` candidates from each
index (200 by default), then combines their ranks when embeddings are available.
Pagination traverses that bounded candidate set; filter-only browsing has no
candidate cap.

Each page requests one extra row to determine whether more results exist.
An opaque cursor carries the next offset and binds it to the query, filters,
owner, effective sort, search mode, ranking settings, active hybrid generation,
and query vector. Reusing it with a different search returns a validation error.
If a provider change produces a different vector, pagination must restart;
the web interface already does this on cursor rejection. This also applies
if the same provider returns different vectors for repeated queries. The cursor
contains only the combined hash and offset, not provider settings or credentials.
Ties use the media ID for deterministic ordering. Pages are live reads, not
a snapshot: imports, edits, and indexing between requests can shift results.

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

The pinned Docbank now exposes `PlanProcessing`, `StartProcessing`,
`ProcessingStatus`, `Rendition`, `DocumentCoverage`, and `SearchDocuments`
through its embedded vault. These operations retain exact-version results and
can run embeddings without a text rendition. Fotobank opens the vault without
processing profiles, so updating the dependency does not enable these jobs or
make provider calls.

The current embedded contract differs from Fotobank's photo-search contract:

- Embedding inputs are original files or rendition text chunks. Fotobank sends
  stripped, downscaled previews. Selecting original-file embeddings would
  change which photo bytes leave the application; it is not an equivalent
  replacement for the current preview policy.
- Search requires an explicit set of 1–4,096 content-version IDs and returns at
  most 100 results without a continuation cursor. Fotobank has larger libraries,
  paginated results, and owner, hidden-media, album, and photographer filters.
  Those filters must constrain candidates before ranking, not just remove
  unauthorized results afterward.
- Embedded processing uses one operator consent scope. Fotobank's owner consent
  and hidden-media policy remain application responsibilities; a shared vault's
  processing grant does not authorize work for every photo owner.
- The processing API does not provide a typed caption/tag annotation surface.
  Extracted rendition text is not a replacement for generated annotations or
  human-curated tags.

The [embedded operations](https://github.com/kenn-io/docbank/blob/main/processing.go),
[request types](https://github.com/kenn-io/docbank/blob/main/types.go), and
[processing service](https://github.com/kenn-io/docbank/blob/main/internal/processing/service.go)
define these upstream boundaries. Follow-up work lives in kata. Fotobank's
current search and AI implementation remains active until its replacement
preserves these product contracts.

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
