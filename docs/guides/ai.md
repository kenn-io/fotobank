# Choose whether to use AI

Import, browsing, albums, backups, and metadata search work without AI. Optional
AI can propose tags and captions and make images searchable by meaning. Generated
descriptions can be wrong; review them rather than treating them as evidence.
Fotobank does not include an autonomous assistant or an MCP server.

## Understand the processing policy first

Fotobank sends downscaled JPEG previews to the configured provider, not camera
RAW files or EXIF blocks. Removing metadata does not make the visible scene
anonymous. Choose a provider whose retention, privacy, and pricing terms the
photo owner accepts. Calls and retries may cost money; Fotobank does not provide
a monetary spending cap.

Hiding a photo is not encryption or cancellation of queued AI work. A photo
hidden after queueing can still be processed after the owner acknowledges this
policy. Do not record that acknowledgment on someone else's behalf without
their explicit approval. [Processing rules](../architecture/search-and-ai.md#failure-and-privacy-rules)
explain this boundary in detail.

## Configure a provider

The operator can configure TOML settings or use the web app's admin AI settings.
Saved admin overrides take precedence over TOML; check the effective settings
when a file change appears to have no effect. Admin access is distinct from
ordinary photo access.

For tags and captions, append these sections to your configuration. Replace
the example URL and model with a service that supports image input through an
OpenAI-compatible chat-completions API:

```toml
[ai]
enabled = true

[ai.vision]
endpoint = "https://provider.example/v1"
api_key_env = "FOTOBANK_VISION_KEY"

[ai.tag]
enabled = true
model = "your-vision-model"

[ai.caption]
enabled = true
model = "your-vision-model"
```

Supply the named secret through the daemon's environment or service manager.
Do not commit it to TOML, paste it into an issue, or include it in agent output.
A variable set only in a later CLI process does not change the already-running
daemon's environment. Validate and restart after changing the file or environment:

```sh
fotobank config validate
fotobank daemon restart
fotobank ai status
```

Status returns JSON. Its vision check requests `/models` from the configured
endpoint; success establishes reachability, not that the chosen model accepts
images. When embeddings are enabled, their health check uses synthetic image
and text inputs.

To test a vision model with a synthetic image, use the vision test in the web
app's admin AI settings (`POST /api/v1/admin/settings/test/vision`). It sends
a chat-completions request using the form's endpoint and model. Review its
result and any model-name warning before queueing photos. Tests may incur
provider charges, but do not use library photos. A successful test does not
mean queued work has finished. An unavailable provider does not prevent
startup or recording consent.

## Approve processing and inspect results

After reviewing the policy, the owner can acknowledge it and queue missing work:

```sh
fotobank ai acknowledge --hidden-processing
fotobank ai backfill --task tag,caption --json
fotobank ai status
```

Acknowledgment allows blocked jobs to become eligible again; it is not just a
prompt for this one backfill. Backfill reports queued counts, not completed
results. Inspect photos and the AI settings page for results and failures.
Use `ai retry-failed --task tag,caption --json` after correcting a provider
problem. Requests are not automatically retried by the CLI; partial task results
can accompany a nonzero exit. See [AI command results](automation.md#queue-ai-work-and-manage-search-generations).

## Add semantic search separately

Embeddings are numeric descriptions that let Fotobank compare a search phrase
with an image. The provider must support both image and text inputs in the
same embedding space; a text-only embedding API is not enough.

Configure `ai.embed.enabled`, `endpoint`, `api_key_env`, `model`, and `dimension`.
The endpoint is the API base URL; Fotobank appends `/embeddings`. The dimension
must match the chosen model. Keep `ai.enabled = true` for processing. Restart,
check `ai status`, and queue `ai backfill --task embed --json` after consent.
Model changes build a new search generation before activation; queued work is
not immediately searchable. Provider failures fall back to metadata search.

## Stop processing

Disable AI and embeddings in the effective settings. If using TOML, set
`ai.enabled = false` and `ai.embed.enabled = false`, then restart the daemon.
If admin overrides exist, change them too. Stopping Fotobank stops its local
workers, but cannot recall data already sent to a provider. Disabling AI does
not delete existing generated results or provider-held data.
