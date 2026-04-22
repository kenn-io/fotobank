-- Owners: the principals who own media on this deployment.
CREATE TABLE owners (
    hub              TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    storage_key      TEXT NOT NULL,
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
    focal_length      TEXT,
    shutter           TEXT,
    width             INTEGER,
    height            INTEGER,
    iso               INTEGER,
    aperture          REAL,

    duration_ms       INTEGER,

    thumb_status      TEXT NOT NULL CHECK (
        thumb_status IN ('pending', 'working', 'ready', 'no_preview', 'failed')
    ),
    thumb_claimed_at  TIMESTAMP,
    thumb_version     INTEGER NOT NULL DEFAULT 0,  -- bumped on every regeneration
    thumb_updated_at  TIMESTAMP,                   -- when thumb_version was last bumped

    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id),
    UNIQUE (owner_hub, owner_user_id, checksum),
    UNIQUE (owner_hub, owner_user_id, path)
);

CREATE INDEX media_owner_timestamp_idx ON media(owner_hub, owner_user_id, timestamp DESC);
CREATE INDEX media_owner_imported_idx  ON media(owner_hub, owner_user_id, imported_at DESC);
CREATE INDEX media_thumb_pending_idx   ON media(thumb_status, thumb_claimed_at)
    WHERE thumb_status IN ('pending', 'working');

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

CREATE TABLE album_media (
    album_id         UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    media_id         UUID NOT NULL REFERENCES media(id)  ON DELETE CASCADE,
    added_at         TIMESTAMP NOT NULL,
    position         INTEGER,
    PRIMARY KEY (album_id, media_id)
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
