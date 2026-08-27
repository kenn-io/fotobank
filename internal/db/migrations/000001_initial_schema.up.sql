-- Owners: the principals who own media on this deployment.
CREATE TABLE owners (
    hub              TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    storage_key      TEXT NOT NULL CHECK (
      length(storage_key) = 36 AND
      storage_key = lower(storage_key) AND
      substr(storage_key, 9, 1) = '-' AND
      substr(storage_key, 14, 1) = '-' AND
      substr(storage_key, 19, 1) = '-' AND
      substr(storage_key, 24, 1) = '-' AND
      length(replace(storage_key, '-', '')) = 32 AND
      replace(storage_key, '-', '') NOT GLOB '*[^0-9a-f]*'
    ),
    display_handle   TEXT,
    created_at       TIMESTAMP NOT NULL,
    PRIMARY KEY (hub, user_id)
);

CREATE UNIQUE INDEX owners_storage_key_uq ON owners(storage_key);

-- Optional display cache for non-owner principals.
CREATE TABLE principal_display (
    hub              TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    handle           TEXT,
    cached_at        TIMESTAMP NOT NULL,
    PRIMARY KEY (hub, user_id)
);

-- Media: photos and videos.
CREATE TABLE media (
    id                UUID PRIMARY KEY,
    owner_hub         TEXT NOT NULL,
    owner_user_id     TEXT NOT NULL,
    media_type        TEXT NOT NULL CHECK (media_type IN ('photo', 'video')),
    mime_type         TEXT NOT NULL,
    path              TEXT NOT NULL,
    original_filename TEXT,
    imported_at       TIMESTAMP NOT NULL,
    timestamp         TIMESTAMP,
    size              INTEGER NOT NULL,
    checksum          TEXT NOT NULL,

    make              TEXT,
    model             TEXT,
    lens_model        TEXT,
    focal_length      TEXT,
    shutter           TEXT,
    width             INTEGER,
    height            INTEGER,
    iso               INTEGER,
    aperture          REAL,

    duration_ms       INTEGER,

    latitude          REAL,
    longitude         REAL,
    gps_at            TIMESTAMP,
    location_label    TEXT,

    -- F2.2 RAW + JPEG pairing.
    -- Root-relative original path captured at import time; substrate
    -- for pair detection.
    import_source_path TEXT NOT NULL DEFAULT '',
    -- FK to JPEG primary; NULL on primaries and standalones.
    -- ON DELETE SET NULL is the referential-integrity floor; the
    -- service layer (§8.7) blocks user-facing deletes when sidecars
    -- exist.
    paired_with_id     UUID REFERENCES media(id) ON DELETE SET NULL,

    thumb_status      TEXT NOT NULL CHECK (
        thumb_status IN ('pending', 'working', 'ready', 'no_preview', 'failed')
    ),
    thumb_claimed_at  TIMESTAMP,
    thumb_version     INTEGER NOT NULL DEFAULT 0,  -- bumped on every regeneration
    thumb_updated_at  TIMESTAMP,                   -- when thumb_version was last bumped

    -- F2.4 Hidden privacy. NULL = visible; non-NULL = hidden, set when the
    -- owner runs Hide. Cascades to sidecars (paired_with_id IS NOT NULL)
    -- by repo.SetHiddenCascade. App-level privacy only (not encryption);
    -- threat model is bystander glance, see specs/2026-04-29-...-design.md.
    hidden_at         TIMESTAMP,

    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id),
    UNIQUE (owner_hub, owner_user_id, checksum),
    UNIQUE (owner_hub, owner_user_id, path),
    CHECK (paired_with_id IS NULL OR paired_with_id <> id)
);

CREATE INDEX media_owner_timestamp_idx ON media(owner_hub, owner_user_id, timestamp DESC);
CREATE INDEX media_owner_imported_idx  ON media(owner_hub, owner_user_id, imported_at DESC);
CREATE INDEX media_thumb_pending_idx   ON media(thumb_status, thumb_claimed_at)
    WHERE thumb_status IN ('pending', 'working');
CREATE INDEX media_owner_geo_idx
    ON media(owner_hub, owner_user_id, latitude, longitude)
    WHERE latitude IS NOT NULL AND longitude IS NOT NULL;
CREATE INDEX media_owner_import_source_path_idx
    ON media(owner_hub, owner_user_id, import_source_path);
-- Sidecar lookup index: GetSidecars / DTO embed path scans by FK.
CREATE INDEX media_paired_with_id_idx
    ON media(paired_with_id) WHERE paired_with_id IS NOT NULL;
-- F2.4 visible-row index: list endpoints (Library, Sessions, album members)
-- always filter hidden_at IS NULL. Partial index keeps that path narrow.
CREATE INDEX media_visible_idx
    ON media(owner_hub, owner_user_id, timestamp DESC)
    WHERE hidden_at IS NULL;
-- Search v1: covers the (owner, type) → visible filter on the search
-- entry path. Partial on hidden_at IS NULL so the index pages stay
-- small and align with how list/search read the table.
CREATE INDEX media_owner_type_idx
    ON media(owner_hub, owner_user_id, media_type)
    WHERE hidden_at IS NULL;
-- Sidebar facets: aggregations on (make || ' ' || model) and lens_model.
-- Owner-scoped, partial-indexed to skip hidden rows and sidecars (which
-- are already excluded by every user-facing list query).
CREATE INDEX media_owner_camera_visible_idx
    ON media(owner_hub, owner_user_id, (make || ' ' || model))
    WHERE hidden_at IS NULL AND paired_with_id IS NULL
        AND make IS NOT NULL AND model IS NOT NULL;
CREATE INDEX media_owner_lens_visible_idx
    ON media(owner_hub, owner_user_id, lens_model)
    WHERE hidden_at IS NULL AND paired_with_id IS NULL
        AND lens_model IS NOT NULL;

-- Owner-consistency triggers on paired_with_id. Mirrors the
-- album_media_owner_consistency_* pair below; defence in depth even
-- though the service-layer pairing pass restricts candidates to one
-- owner per (owner, directory) group.
--
-- Three triggers cover the matrix:
--   * insert  — sidecar row points at primary owned by another principal.
--   * update  — sidecar row's owner or paired_with_id is changed and
--               diverges from the referenced primary's owner.
--   * primary-update — primary's owner_hub/owner_user_id is changed
--               while sidecars still reference it. The two earlier
--               triggers gate the sidecar side; this one closes the
--               loop on the primary side.
CREATE TRIGGER media_paired_with_owner_consistency_insert
BEFORE INSERT ON media
FOR EACH ROW
WHEN NEW.paired_with_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_hub
          OR (SELECT owner_user_id FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_user_id
        THEN RAISE(ABORT, 'sidecar and primary must share owner')
    END;
END;

CREATE TRIGGER media_paired_with_owner_consistency_update
BEFORE UPDATE OF paired_with_id, owner_hub, owner_user_id ON media
FOR EACH ROW
WHEN NEW.paired_with_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_hub
          OR (SELECT owner_user_id FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_user_id
        THEN RAISE(ABORT, 'sidecar and primary must share owner')
    END;
END;

CREATE TRIGGER media_paired_with_owner_consistency_primary_update
BEFORE UPDATE OF owner_hub, owner_user_id ON media
FOR EACH ROW
WHEN (NEW.owner_hub != OLD.owner_hub OR NEW.owner_user_id != OLD.owner_user_id)
     AND EXISTS (SELECT 1 FROM media WHERE paired_with_id = NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'cannot change primary owner while sidecars reference it');
END;

-- Final-shaped media domain. F02a leaves active product paths on media while
-- these tables establish the Docbank-backed asset and file contract.
CREATE TABLE assets (
    id                UUID PRIMARY KEY,
    owner_hub         TEXT NOT NULL,
    owner_user_id     TEXT NOT NULL,
    state             TEXT NOT NULL
                      CHECK (state IN ('pending', 'ready', 'conflict')),
    media_type        TEXT NOT NULL
                      CHECK (media_type IN ('photo', 'video')),
    imported_at       TIMESTAMP NOT NULL,
    timestamp         TIMESTAMP,
    make              TEXT,
    model             TEXT,
    lens_model        TEXT,
    focal_length      TEXT,
    shutter           TEXT,
    width             INTEGER,
    height            INTEGER,
    iso               INTEGER,
    aperture          REAL,
    duration_ms       INTEGER,
    latitude          REAL,
    longitude         REAL,
    gps_at            TIMESTAMP,
    location_label    TEXT,
    thumb_status      TEXT NOT NULL CHECK (
        thumb_status IN ('pending', 'working', 'ready',
                         'no_preview', 'failed')
    ),
    thumb_claimed_at  TIMESTAMP,
    thumb_version     INTEGER NOT NULL DEFAULT 0,
    thumb_updated_at  TIMESTAMP,
    hidden_at         TIMESTAMP,
    FOREIGN KEY (owner_hub, owner_user_id)
      REFERENCES owners(hub, user_id),
    UNIQUE (id, owner_hub, owner_user_id)
);

CREATE TABLE media_files (
    id                    UUID PRIMARY KEY,
    asset_id              UUID NOT NULL,
    owner_hub             TEXT NOT NULL,
    owner_user_id         TEXT NOT NULL,
    role                  TEXT NOT NULL CHECK (
        role IN ('primary', 'original', 'sidecar', 'alternate')
    ),
    mime_type             TEXT NOT NULL,
    original_filename     TEXT NOT NULL,
    import_source_path    TEXT NOT NULL DEFAULT '',
    size                  INTEGER NOT NULL CHECK (size >= 0),
    docbank_node_id       INTEGER,
    docbank_virtual_path  TEXT,
    current_version_id    TEXT,
    sha256                TEXT,
    FOREIGN KEY (asset_id, owner_hub, owner_user_id)
      REFERENCES assets(id, owner_hub, owner_user_id) ON DELETE CASCADE,
    CHECK (
      (docbank_node_id IS NULL AND docbank_virtual_path IS NULL AND
       current_version_id IS NULL AND sha256 IS NULL) OR
      (docbank_node_id IS NOT NULL AND docbank_node_id > 0 AND
       docbank_virtual_path IS NOT NULL AND
       length(docbank_virtual_path) > 1 AND
       substr(docbank_virtual_path, 1, 1) = '/' AND
       docbank_virtual_path = trim(docbank_virtual_path) AND
       instr(docbank_virtual_path, char(0)) = 0 AND
       instr(docbank_virtual_path, char(92)) = 0 AND
       docbank_virtual_path NOT LIKE '%//%' AND
       docbank_virtual_path NOT LIKE '%/./%' AND
       docbank_virtual_path NOT LIKE '%/../%' AND
       substr(docbank_virtual_path, -2) <> '/.' AND
       substr(docbank_virtual_path, -3) <> '/..' AND
       substr(docbank_virtual_path, -1) <> '/' AND
       docbank_virtual_path LIKE '/owners/%/media/' || id || '/%' AND
       length(docbank_virtual_path) -
         length(replace(docbank_virtual_path, '/', '')) = 5 AND
       current_version_id IS NOT NULL AND
       length(current_version_id) = 36 AND
       current_version_id = lower(current_version_id) AND
       substr(current_version_id, 9, 1) = '-' AND
       substr(current_version_id, 14, 1) = '-' AND
       substr(current_version_id, 15, 1) = '4' AND
       substr(current_version_id, 19, 1) = '-' AND
       substr(current_version_id, 20, 1) GLOB '[89ab]' AND
       substr(current_version_id, 24, 1) = '-' AND
       length(replace(current_version_id, '-', '')) = 32 AND
       replace(current_version_id, '-', '') NOT GLOB '*[^0-9a-f]*' AND
       sha256 IS NOT NULL AND
       length(sha256) = 64 AND sha256 = lower(sha256) AND
       sha256 NOT GLOB '*[^0-9a-f]*')
    )
);

CREATE UNIQUE INDEX media_files_one_primary_uq
  ON media_files(asset_id) WHERE role = 'primary';
CREATE UNIQUE INDEX media_files_docbank_node_uq
  ON media_files(docbank_node_id) WHERE docbank_node_id IS NOT NULL;
CREATE UNIQUE INDEX media_files_docbank_path_uq
  ON media_files(docbank_virtual_path)
  WHERE docbank_virtual_path IS NOT NULL;
CREATE UNIQUE INDEX media_files_current_version_uq
  ON media_files(current_version_id) WHERE current_version_id IS NOT NULL;
CREATE INDEX media_files_asset_idx ON media_files(asset_id, role, id);

CREATE TRIGGER assets_ready_insert
BEFORE INSERT ON assets
FOR EACH ROW
WHEN NEW.state = 'ready'
BEGIN
    SELECT RAISE(ABORT, 'asset graphs start pending');
END;

CREATE TRIGGER assets_ready_primary_update
BEFORE UPDATE OF state ON assets
FOR EACH ROW
WHEN NEW.state = 'ready' AND (
    SELECT COUNT(*) FROM media_files
    WHERE asset_id = NEW.id AND role = 'primary'
) <> 1
BEGIN
    SELECT RAISE(ABORT, 'ready asset requires exactly one primary');
END;

CREATE TRIGGER assets_ready_mapping_update
BEFORE UPDATE OF state ON assets
FOR EACH ROW
WHEN NEW.state = 'ready' AND EXISTS (
    SELECT 1 FROM media_files
    WHERE asset_id = NEW.id AND docbank_node_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'ready asset requires mapped files');
END;

CREATE TRIGGER media_files_ready_primary_delete
BEFORE DELETE ON media_files
FOR EACH ROW
WHEN OLD.role = 'primary' AND EXISTS (
    SELECT 1 FROM assets WHERE id = OLD.asset_id AND state = 'ready'
)
BEGIN
    SELECT RAISE(ABORT, 'cannot remove primary from ready asset');
END;

CREATE TRIGGER media_files_ready_primary_update
BEFORE UPDATE OF role ON media_files
FOR EACH ROW
WHEN OLD.role = 'primary' AND NEW.role <> 'primary' AND EXISTS (
    SELECT 1 FROM assets WHERE id = OLD.asset_id AND state = 'ready'
)
BEGIN
    SELECT RAISE(ABORT, 'cannot remove primary from ready asset');
END;

CREATE TRIGGER media_files_coordinate_update
BEFORE UPDATE OF asset_id, owner_hub, owner_user_id ON media_files
FOR EACH ROW
WHEN NEW.asset_id <> OLD.asset_id
  OR NEW.owner_hub <> OLD.owner_hub
  OR NEW.owner_user_id <> OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'file asset and owner are immutable');
END;

CREATE TRIGGER media_files_ready_mapping_insert
BEFORE INSERT ON media_files
FOR EACH ROW
WHEN NEW.docbank_node_id IS NULL AND EXISTS (
    SELECT 1 FROM assets WHERE id = NEW.asset_id AND state = 'ready'
)
BEGIN
    SELECT RAISE(ABORT, 'ready asset requires mapped files');
END;

CREATE TRIGGER media_files_ready_mapping_update
BEFORE UPDATE OF docbank_node_id, docbank_virtual_path,
                 current_version_id, sha256 ON media_files
FOR EACH ROW
WHEN NEW.docbank_node_id IS NULL AND EXISTS (
    SELECT 1 FROM assets WHERE id = NEW.asset_id AND state = 'ready'
)
BEGIN
    SELECT RAISE(ABORT, 'ready asset requires mapped files');
END;

CREATE TABLE media_file_relationships (
    source_file_id UUID NOT NULL
      REFERENCES media_files(id) ON DELETE CASCADE,
    target_file_id UUID NOT NULL
      REFERENCES media_files(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (
      kind IN ('sidecar_of', 'derived_from', 'paired_with')
    ),
    PRIMARY KEY (source_file_id, target_file_id, kind),
    CHECK (source_file_id <> target_file_id)
);

CREATE INDEX media_file_relationships_target_idx
  ON media_file_relationships(target_file_id, kind, source_file_id);

CREATE TRIGGER media_file_relationships_consistency_insert
BEFORE INSERT ON media_file_relationships
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM media_files AS source
    JOIN media_files AS target ON target.id = NEW.target_file_id
    WHERE source.id = NEW.source_file_id
      AND source.asset_id = target.asset_id
      AND source.owner_hub = target.owner_hub
      AND source.owner_user_id = target.owner_user_id
)
BEGIN
    SELECT RAISE(ABORT, 'relationship files must share asset and owner');
END;

CREATE TRIGGER media_file_relationships_consistency_update
BEFORE UPDATE OF source_file_id, target_file_id ON media_file_relationships
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM media_files AS source
    JOIN media_files AS target ON target.id = NEW.target_file_id
    WHERE source.id = NEW.source_file_id
      AND source.asset_id = target.asset_id
      AND source.owner_hub = target.owner_hub
      AND source.owner_user_id = target.owner_user_id
)
BEGIN
    SELECT RAISE(ABORT, 'relationship files must share asset and owner');
END;

-- Albums.
CREATE TABLE albums (
    id               UUID PRIMARY KEY,
    owner_hub        TEXT NOT NULL,
    owner_user_id    TEXT NOT NULL,
    name             TEXT NOT NULL,
    created_at       TIMESTAMP NOT NULL,
    updated_at       TIMESTAMP NOT NULL,
    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id)
);

CREATE INDEX albums_owner_idx ON albums(owner_hub, owner_user_id, name);
-- Covers ListByOwner's ORDER BY updated_at DESC, id after owner filter.
CREATE INDEX albums_owner_updated_idx
    ON albums(owner_hub, owner_user_id, updated_at DESC, id);

CREATE TABLE album_media (
    album_id         UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    media_id         UUID NOT NULL REFERENCES media(id)  ON DELETE CASCADE,
    added_at         TIMESTAMP NOT NULL,
    position         INTEGER,
    PRIMARY KEY (album_id, media_id)
);
-- Covers the cover subquery and ListMedia sort_by=added
-- (most-recent-added-first per album).
CREATE INDEX album_media_album_added_idx
    ON album_media(album_id, added_at DESC);

-- Per-user, non-secret UI preferences.
-- Keys are dotted strings (e.g. "theme", "density.library"); values are JSON.
CREATE TABLE user_settings (
    principal_hub        TEXT NOT NULL,
    principal_user_id    TEXT NOT NULL,
    key                  TEXT NOT NULL,
    value                TEXT NOT NULL,
    updated_at           TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id, key)
);

-- Server-global, non-secret runtime settings. Values are JSON-encoded.
CREATE TABLE app_settings (
    key                 TEXT PRIMARY KEY,
    value               TEXT NOT NULL,
    updated_at          TIMESTAMP NOT NULL,
    updated_by_hub      TEXT,
    updated_by_user_id  TEXT
);

-- Owner-consistency trigger on album_media inserts / updates.
CREATE TRIGGER album_media_owner_consistency_insert
BEFORE INSERT ON album_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_hub FROM media  WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_user_id FROM media  WHERE id = NEW.media_id)
        THEN RAISE(ABORT, 'album and media must share owner')
    END;
END;

CREATE TRIGGER album_media_owner_consistency_update
BEFORE UPDATE OF album_id, media_id ON album_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_hub FROM media  WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_user_id FROM media  WHERE id = NEW.media_id)
        THEN RAISE(ABORT, 'album and media must share owner')
    END;
END;

-- Scopes: grants minted by owners, registered with the broker.
CREATE TABLE scopes (
    uuid                 UUID PRIMARY KEY,
    owner_hub            TEXT NOT NULL,
    owner_user_id        TEXT NOT NULL,
    grantee_hub          TEXT NOT NULL,
    grantee_user_id      TEXT NOT NULL,
    target_type          TEXT NOT NULL CHECK (target_type IN ('album_live', 'media_set')),
    target_album_id      UUID REFERENCES albums(id),
    allow_download       BOOLEAN NOT NULL DEFAULT 0,
    label                TEXT,
    created_at           TIMESTAMP NOT NULL,
    expires_at           TIMESTAMP,
    revoked_at           TIMESTAMP,

    broker_status        TEXT NOT NULL CHECK (broker_status IN (
        'pending', 'active', 'failed', 'revoking', 'revoked_remote'
    )),
    broker_registered_at TIMESTAMP,
    broker_granted_at    TIMESTAMP,
    broker_revoked_at    TIMESTAMP,
    broker_last_error    TEXT,
    broker_attempts      INTEGER NOT NULL DEFAULT 0,
    -- Earliest moment the share worker should re-poll this row. NULL = poll immediately.
    broker_next_attempt_at TIMESTAMP,

    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id),
    CHECK (
        (target_type = 'album_live'  AND target_album_id IS NOT NULL) OR
        (target_type = 'media_set'   AND target_album_id IS NULL)
    )
);

CREATE INDEX scopes_grantee_idx        ON scopes(grantee_hub, grantee_user_id) WHERE revoked_at IS NULL;
CREATE INDEX scopes_owner_idx          ON scopes(owner_hub, owner_user_id) WHERE revoked_at IS NULL;
-- Worker poll index. 'failed' is intentionally excluded — it's a
-- terminal state awaiting Retry; including it would keep the worker
-- polling rows that should be inert until operator intervention.
CREATE INDEX scopes_broker_pending_idx ON scopes(broker_status, broker_attempts)
    WHERE broker_status IN ('pending', 'revoking');
-- Worker poll ordering index. Partial to keep it tiny: only rows the
-- worker might act on (pending or revoking). Ordered by
-- broker_next_attempt_at so SELECT ... LIMIT N returns due rows first.
CREATE INDEX scopes_broker_ready_idx
    ON scopes(broker_next_attempt_at)
    WHERE broker_status IN ('pending', 'revoking');

CREATE TABLE scope_media (
    scope_uuid       UUID NOT NULL REFERENCES scopes(uuid) ON DELETE CASCADE,
    media_id         UUID NOT NULL REFERENCES media(id)    ON DELETE CASCADE,
    PRIMARY KEY (scope_uuid, media_id)
);

-- Owner-consistency trigger on scope_media inserts / updates.
CREATE TRIGGER scope_media_owner_consistency_insert
BEFORE INSERT ON scope_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_hub FROM media  WHERE id   = NEW.media_id)
          OR (SELECT owner_user_id FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_user_id FROM media  WHERE id   = NEW.media_id)
        THEN RAISE(ABORT, 'scope and media must share owner')
    END;
END;

CREATE TRIGGER scope_media_owner_consistency_update
BEFORE UPDATE OF scope_uuid, media_id ON scope_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_hub FROM media  WHERE id   = NEW.media_id)
          OR (SELECT owner_user_id FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_user_id FROM media  WHERE id   = NEW.media_id)
        THEN RAISE(ABORT, 'scope and media must share owner')
    END;
END;

-- Owner-consistency trigger on scopes.target_album_id: an album_live
-- scope must reference an album owned by the same principal as the
-- scope. Fires on INSERT and on UPDATE of the relevant columns.
CREATE TRIGGER scopes_target_album_owner_consistency_insert
BEFORE INSERT ON scopes
FOR EACH ROW
WHEN NEW.target_album_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_hub
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_user_id
        THEN RAISE(ABORT, 'scope and target album must share owner')
    END;
END;

CREATE TRIGGER scopes_target_album_owner_consistency_update
BEFORE UPDATE OF owner_hub, owner_user_id, target_album_id ON scopes
FOR EACH ROW
WHEN NEW.target_album_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_hub
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_user_id
        THEN RAISE(ABORT, 'scope and target album must share owner')
    END;
END;

-- F2.4 Hidden privacy auth tables. Per-owner passcode credential, opaque
-- unlock-cookie sessions, sliding-window failure log, and persisted
-- lockout decision. All FK to owners(hub, user_id) so a CLI tool that
-- removes an owner row also tears down their hidden state.
CREATE TABLE auth_hidden_credential (
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    passcode_hash      TEXT NOT NULL,             -- argon2id encoded string
    created_at         TIMESTAMP NOT NULL,
    updated_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id),
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id) ON DELETE CASCADE
);

CREATE TABLE auth_hidden_session (
    token_sha256       BLOB NOT NULL PRIMARY KEY,
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    issued_at          TIMESTAMP NOT NULL,
    expires_at         TIMESTAMP NOT NULL,
    revoked_at         TIMESTAMP,
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id) ON DELETE CASCADE
);
-- Active-session lookup by principal: powers revoke-all-for-principal on
-- Change/Disable/AdminReset.
CREATE INDEX auth_hidden_session_principal_active_idx
    ON auth_hidden_session(principal_hub, principal_user_id)
    WHERE revoked_at IS NULL;
-- Expiry sweep index: background sweeper closes expired-but-unrevoked rows.
CREATE INDEX auth_hidden_session_expiry_idx
    ON auth_hidden_session(expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE auth_hidden_failure (
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    occurred_at        TIMESTAMP NOT NULL,
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id) ON DELETE CASCADE
);
CREATE INDEX auth_hidden_failure_owner_idx
    ON auth_hidden_failure(principal_hub, principal_user_id, occurred_at DESC);

CREATE TABLE auth_hidden_lockout (
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    locked_until       TIMESTAMP NOT NULL,
    updated_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id),
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id) ON DELETE CASCADE
);

-- ============================================================
-- AI: tag and caption pipeline.
-- See docs/superpowers/specs/2026-04-30-fotobank-ai-tag-caption-design.md.
-- ============================================================

-- One row per task run (or in-flight insert that gets staled on retry).
CREATE TABLE ai_results (
    id              UUID PRIMARY KEY,
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL CHECK (task IN ('tag','caption','embed')),
    model_id        TEXT NOT NULL,
    prompt_version  TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL,
    input_profile   TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('active','stale')),
    generated_at    TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX ai_results_active_one_per
    ON ai_results(media_id, task) WHERE status = 'active';
CREATE INDEX ai_results_media_task_idx
    ON ai_results(media_id, task, status);

CREATE TABLE media_tags (
    result_id  UUID NOT NULL REFERENCES ai_results(id) ON DELETE CASCADE,
    tag_key    TEXT NOT NULL,
    tag_label  TEXT NOT NULL,
    rank       INTEGER NOT NULL,
    PRIMARY KEY (result_id, tag_key)
);
CREATE INDEX media_tags_key_idx ON media_tags(tag_key);

CREATE TABLE media_captions (
    result_id  UUID PRIMARY KEY REFERENCES ai_results(id) ON DELETE CASCADE,
    text       TEXT NOT NULL
);

CREATE TABLE ai_jobs (
    id              UUID PRIMARY KEY,
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL CHECK (task IN ('tag','caption','embed')),
    fingerprint     TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (
                      status IN ('pending','working','blocked','done','failed','superseded')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    last_error_kind TEXT,
    claimed_at      TIMESTAMP,
    enqueued_at     TIMESTAMP NOT NULL,
    completed_at    TIMESTAMP
);
CREATE UNIQUE INDEX ai_jobs_active_idx
    ON ai_jobs(media_id, task) WHERE status IN ('pending','working','blocked');
CREATE INDEX ai_jobs_pending_idx
    ON ai_jobs(task, status, claimed_at) WHERE status IN ('pending','working','blocked');
CREATE INDEX ai_jobs_terminal_idx
    ON ai_jobs(task, status, completed_at) WHERE status IN ('done','failed','superseded');

CREATE TABLE ai_failures (
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL CHECK (task IN ('tag','caption','embed')),
    model_id        TEXT NOT NULL,
    prompt_version  TEXT NOT NULL,
    input_profile   TEXT NOT NULL,
    last_error      TEXT NOT NULL,
    last_error_kind TEXT NOT NULL,
    attempt_count   INTEGER NOT NULL,
    failed_at       TIMESTAMP NOT NULL,
    PRIMARY KEY (media_id, task, model_id, prompt_version, input_profile)
);
CREATE INDEX ai_failures_active_idx
    ON ai_failures(task, model_id, prompt_version, input_profile, failed_at DESC);

CREATE TABLE ai_skipped (
    media_id     UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task         TEXT NOT NULL CHECK (task IN ('tag','caption','embed')),
    reason       TEXT NOT NULL,
    recorded_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (media_id, task)
);

-- ============================================================
-- Search v1: embedding generations and per-media vec mapping.
-- See docs/superpowers/specs/2026-05-01-fotobank-search-design.md §5.3-§5.4.
-- ============================================================

CREATE TABLE embedding_generations (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint      TEXT    NOT NULL,
    fingerprint_hash TEXT    NOT NULL UNIQUE,
    model_id         TEXT    NOT NULL,
    input_profile    TEXT    NOT NULL,
    vec_table_name   TEXT    NOT NULL UNIQUE,
    dimension        INTEGER NOT NULL,
    state            TEXT    NOT NULL CHECK(state IN ('building','active','retired')),
    embedded_count   INTEGER NOT NULL DEFAULT 0,
    threshold_pct    INTEGER NOT NULL DEFAULT 95,
    created_at       TIMESTAMP NOT NULL,
    activated_at     TIMESTAMP,
    retired_at       TIMESTAMP
);

CREATE UNIQUE INDEX embedding_generations_one_active
    ON embedding_generations(state) WHERE state = 'active';

CREATE TABLE media_embedding_ids (
    generation_id INTEGER NOT NULL REFERENCES embedding_generations(id) ON DELETE CASCADE,
    media_id      UUID    NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    vec_id        INTEGER NOT NULL,
    PRIMARY KEY (generation_id, media_id),
    UNIQUE (generation_id, vec_id)
);
CREATE INDEX media_embedding_ids_media_idx
    ON media_embedding_ids(media_id);

-- ============================================================
-- Search v1 — FTS5 lexical index over the §7 corpus.
-- ============================================================

CREATE VIRTUAL TABLE media_fts USING fts5(
    media_id UNINDEXED,
    caption_text,
    tag_label,
    filename,
    camera,
    lens,
    location_label,
    tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER media_fts_cleanup_after_delete
AFTER DELETE ON media
FOR EACH ROW
BEGIN
    DELETE FROM media_fts WHERE media_id = OLD.id;
END;
