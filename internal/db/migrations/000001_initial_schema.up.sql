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

-- Product assets and their Docbank-backed physical files.
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

CREATE INDEX assets_owner_timestamp_idx
  ON assets(owner_hub, owner_user_id, timestamp DESC);
CREATE INDEX assets_owner_imported_idx
  ON assets(owner_hub, owner_user_id, imported_at DESC);
CREATE INDEX assets_thumb_pending_idx
  ON assets(thumb_status, thumb_claimed_at)
  WHERE thumb_status IN ('pending', 'working');
CREATE INDEX assets_owner_geo_idx
  ON assets(owner_hub, owner_user_id, latitude, longitude)
  WHERE latitude IS NOT NULL AND longitude IS NOT NULL;
CREATE INDEX assets_visible_idx
  ON assets(owner_hub, owner_user_id, timestamp DESC)
  WHERE hidden_at IS NULL AND state = 'ready';
CREATE INDEX assets_owner_type_idx
  ON assets(owner_hub, owner_user_id, media_type)
  WHERE hidden_at IS NULL AND state = 'ready';
CREATE INDEX assets_owner_camera_visible_idx
  ON assets(owner_hub, owner_user_id, (make || ' ' || model))
  WHERE hidden_at IS NULL AND state = 'ready'
    AND make IS NOT NULL AND model IS NOT NULL;
CREATE INDEX assets_owner_lens_visible_idx
  ON assets(owner_hub, owner_user_id, lens_model)
  WHERE hidden_at IS NULL AND state = 'ready' AND lens_model IS NOT NULL;

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
CREATE UNIQUE INDEX media_files_owner_sha256_uq
  ON media_files(owner_hub, owner_user_id, sha256)
  WHERE sha256 IS NOT NULL;

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

-- Durable ledger for the SQLite -> Docbank half of an import.
CREATE TABLE content_operations (
    id                    UUID PRIMARY KEY,
    asset_id              UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    file_id               UUID NOT NULL UNIQUE REFERENCES media_files(id) ON DELETE CASCADE,
    owner_hub             TEXT NOT NULL,
    owner_user_id         TEXT NOT NULL,
    status                TEXT NOT NULL CHECK (
      status IN ('pending', 'applied', 'conflict')
    ),
    expected_sha256       TEXT NOT NULL CHECK (
      length(expected_sha256) = 64 AND
      expected_sha256 = lower(expected_sha256) AND
      expected_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    expected_size         INTEGER NOT NULL CHECK (expected_size >= 0),
    docbank_virtual_path  TEXT NOT NULL,
    docbank_node_id       INTEGER,
    docbank_version_id    TEXT,
    last_error            TEXT,
    created_at            TIMESTAMP NOT NULL,
    updated_at            TIMESTAMP NOT NULL,
    FOREIGN KEY (asset_id, owner_hub, owner_user_id)
      REFERENCES assets(id, owner_hub, owner_user_id),
    CHECK (
      (status = 'pending' AND docbank_node_id IS NULL AND docbank_version_id IS NULL) OR
      (status = 'applied' AND docbank_node_id > 0 AND docbank_version_id IS NOT NULL) OR
      (status = 'conflict')
    )
);

CREATE INDEX content_operations_pending_idx
  ON content_operations(owner_hub, owner_user_id, status, created_at)
  WHERE status = 'pending';

CREATE UNIQUE INDEX content_operations_owner_sha256_uq
  ON content_operations(owner_hub, owner_user_id, expected_sha256);

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
    media_id         UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    added_at         TIMESTAMP NOT NULL,
    position         INTEGER,
    PRIMARY KEY (album_id, media_id)
);
-- Covers the cover subquery and ListMedia sort_by=added
-- (most-recent-added-first per album).
CREATE INDEX album_media_album_added_idx
    ON album_media(album_id, added_at DESC);

-- Writable working copies materialized from exact Docbank versions.
CREATE TABLE checkouts (
    id              UUID PRIMARY KEY,
    owner_hub       TEXT NOT NULL,
    owner_user_id   TEXT NOT NULL,
    root            TEXT NOT NULL,
    layout          TEXT NOT NULL CHECK (layout IN ('capture_date')),
    include_all     INTEGER NOT NULL DEFAULT 0 CHECK (include_all IN (0, 1)),
    state           TEXT NOT NULL CHECK (state IN ('building', 'active', 'error')),
    last_error      TEXT,
    created_at      TIMESTAMP NOT NULL,
    updated_at      TIMESTAMP NOT NULL,
    FOREIGN KEY (owner_hub, owner_user_id)
      REFERENCES owners(hub, user_id)
);

CREATE INDEX checkouts_owner_idx
  ON checkouts(owner_hub, owner_user_id, created_at, id);

CREATE UNIQUE INDEX checkouts_live_root_uq ON checkouts(root)
  WHERE state IN ('building', 'active');

-- Source identifiers below are historical snapshots, not ownership links.
-- Deleting an asset, album, or media file must not erase a checkout ledger.
CREATE TABLE checkout_asset_selections (
    checkout_id     UUID NOT NULL REFERENCES checkouts(id) ON DELETE CASCADE,
    asset_id        UUID NOT NULL,
    PRIMARY KEY (checkout_id, asset_id)
);

CREATE TABLE checkout_album_selections (
    checkout_id     UUID NOT NULL REFERENCES checkouts(id) ON DELETE CASCADE,
    album_id        UUID NOT NULL,
    PRIMARY KEY (checkout_id, album_id)
);

CREATE TABLE checkout_year_selections (
    checkout_id     UUID NOT NULL REFERENCES checkouts(id) ON DELETE CASCADE,
    start_year      INTEGER NOT NULL CHECK (start_year BETWEEN 1 AND 9999),
    end_year        INTEGER NOT NULL CHECK (end_year BETWEEN start_year AND 9999),
    PRIMARY KEY (checkout_id, start_year, end_year)
);

CREATE TABLE checkout_entries (
    checkout_id       UUID NOT NULL REFERENCES checkouts(id) ON DELETE CASCADE,
    file_id           UUID NOT NULL,
    relative_path     TEXT NOT NULL,
    base_version_id   TEXT NOT NULL,
    base_sha256       TEXT NOT NULL CHECK (
      length(base_sha256) = 64 AND base_sha256 = lower(base_sha256) AND
      base_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    base_size         INTEGER NOT NULL CHECK (base_size >= 0),
    observed_size     INTEGER NOT NULL CHECK (observed_size >= 0),
    observed_mtime    TIMESTAMP NOT NULL,
    observed_identity TEXT NOT NULL,
    observed_sha256   TEXT NOT NULL CHECK (
      length(observed_sha256) = 64 AND observed_sha256 = lower(observed_sha256) AND
      observed_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    state             TEXT NOT NULL CHECK (
      state IN ('clean', 'pending', 'conflict', 'missing', 'error')
    ),
    last_error        TEXT,
    created_at        TIMESTAMP NOT NULL,
    updated_at        TIMESTAMP NOT NULL,
    PRIMARY KEY (checkout_id, file_id),
    UNIQUE (checkout_id, relative_path),
    CHECK (
      length(relative_path) > 0 AND
      substr(relative_path, 1, 1) <> '/' AND
      instr(relative_path, char(0)) = 0 AND
      instr(relative_path, char(92)) = 0 AND
      relative_path NOT LIKE '%//%' AND
      relative_path NOT LIKE '%/./%' AND
      relative_path NOT LIKE '%/../%' AND
      substr(relative_path, -2) <> '/.' AND
      substr(relative_path, -3) <> '/..' AND
      substr(relative_path, -1) <> '/'
    )
);

CREATE INDEX checkout_entries_state_idx
  ON checkout_entries(checkout_id, state, relative_path);

CREATE UNIQUE INDEX checkout_entries_binding_uq
  ON checkout_entries(checkout_id, file_id, relative_path);

-- Durable observations let the periodic scanner settle local changes across
-- process restarts. A NULL file_id is an untracked working file; a non-NULL
-- file_id must name the tracked checkout entry at the same path.
CREATE TABLE checkout_scan_candidates (
    checkout_id       UUID NOT NULL REFERENCES checkouts(id) ON DELETE CASCADE,
    relative_path     TEXT NOT NULL,
    file_id           UUID,
    observed_size     INTEGER NOT NULL CHECK (observed_size >= 0),
    observed_mtime    TIMESTAMP NOT NULL,
    observed_identity TEXT NOT NULL,
    observed_sha256   TEXT CHECK (
      observed_sha256 IS NULL OR (
        length(observed_sha256) = 64 AND
        observed_sha256 = lower(observed_sha256) AND
        observed_sha256 NOT GLOB '*[^0-9a-f]*'
      )
    ),
    state             TEXT NOT NULL CHECK (state IN ('settling', 'pending')),
    first_observed_at TIMESTAMP NOT NULL,
    last_observed_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (checkout_id, relative_path),
    FOREIGN KEY (checkout_id, file_id, relative_path)
      REFERENCES checkout_entries(checkout_id, file_id, relative_path) ON DELETE CASCADE,
    CHECK (
      length(relative_path) > 0 AND
      substr(relative_path, 1, 1) <> '/' AND
      instr(relative_path, char(0)) = 0 AND
      instr(relative_path, char(92)) = 0 AND
      relative_path NOT LIKE '%//%' AND
      relative_path NOT LIKE '%/./%' AND
      relative_path NOT LIKE '%/../%' AND
      substr(relative_path, -2) <> '/.' AND
      substr(relative_path, -3) <> '/..' AND
      substr(relative_path, -1) <> '/'
    )
);

CREATE INDEX checkout_scan_candidates_state_idx
  ON checkout_scan_candidates(state, checkout_id, relative_path);

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
             (SELECT owner_hub FROM assets WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_user_id FROM assets WHERE id = NEW.media_id)
        THEN RAISE(ABORT, 'album and media must share owner')
    END;
END;

CREATE TRIGGER album_media_owner_consistency_update
BEFORE UPDATE OF album_id, media_id ON album_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_hub FROM assets WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_user_id FROM assets WHERE id = NEW.media_id)
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
    media_id         UUID NOT NULL REFERENCES assets(id)   ON DELETE CASCADE,
    PRIMARY KEY (scope_uuid, media_id)
);

-- Owner-consistency trigger on scope_media inserts / updates.
CREATE TRIGGER scope_media_owner_consistency_insert
BEFORE INSERT ON scope_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_hub FROM assets WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_user_id FROM assets WHERE id = NEW.media_id)
        THEN RAISE(ABORT, 'scope and media must share owner')
    END;
END;

CREATE TRIGGER scope_media_owner_consistency_update
BEFORE UPDATE OF scope_uuid, media_id ON scope_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_hub FROM assets WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_user_id FROM assets WHERE id = NEW.media_id)
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
-- See docs/architecture/search-and-ai.md.
-- ============================================================

-- One row per task run (or in-flight insert that gets staled on retry).
CREATE TABLE ai_results (
    id              UUID PRIMARY KEY,
    media_id        UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
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
    media_id        UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
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
    media_id        UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
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
    media_id     UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    task         TEXT NOT NULL CHECK (task IN ('tag','caption','embed')),
    reason       TEXT NOT NULL,
    recorded_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (media_id, task)
);

-- ============================================================
-- Search: embedding generations and per-media vector mapping.
-- See docs/architecture/search-and-ai.md.
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
    media_id      UUID    NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
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
AFTER DELETE ON assets
FOR EACH ROW
BEGIN
    DELETE FROM media_fts WHERE media_id = OLD.id;
END;
