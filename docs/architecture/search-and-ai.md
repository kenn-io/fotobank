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

The current implementation keeps vectors, prompts, job queues, and search
lifecycle in Fotobank. Source projections use Fotobank media/asset identity and
exact input versions. Docbank already supplies source extraction, including
camera evidence, and canonical image previews through `internal/content`.

The accepted boundary places reusable model outputs, embeddings, and retrieval
in Docbank. Fotobank will consume those results and apply photographer-facing
curation, ownership, visibility, and query behavior. That integration is not
implemented yet; the packages above describe the code that runs today.

## Failure and privacy rules

- Gateway probes are bounded and never make health endpoints enqueue work.
- Provider checks run on AI status requests, not daemon startup or consent
  recording. Vision and embedding probes use the current runtime settings,
  including endpoint and credentials, after admin settings changes. With AI
  and embeddings enabled, `embed.provider` reports the embedding endpoint's
  image/text probe result. The daemon shares successful and failed embedding
  results for 30 seconds and allows only one check at a time, with a five-second
  total timeout, independent of the initiating caller's cancellation. Endpoint,
  credential, model, dimension, or timeout changes invalidate that result.
  `last_check_at` records the actual check, not the
  cache read; owner consent and queue counts are still read fresh. The probe uses
  synthetic inputs; it never reads photos. Outages and provider-side errors do not prevent
  the daemon from serving diagnostics or recording consent. Local configuration
  validation still rejects invalid URLs, missing models, and invalid dimensions.
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
