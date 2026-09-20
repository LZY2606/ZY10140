package store

// Schema for the sounding application.
//
// Versioning model:
//   - raw_packets is append-only evidence; nothing ever updates its bytes.
//   - every ingest after the first derives a new draft *version*. Re-running
//     the algorithm therefore creates new rows and cannot rewrite a
//     published version (versions.published_at is set exactly once).
//   - manual judgments live in judgments, separate from auto findings; the
//     assembly step reads judgments as overrides but never deletes them.
const Schema = `
CREATE TABLE IF NOT EXISTS soundings (
    id          TEXT PRIMARY KEY,
    device      TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS raw_packets (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    sounding_id     TEXT NOT NULL REFERENCES soundings(id),
    device          TEXT NOT NULL,
    seq             INTEGER NOT NULL,
    obs_time        TEXT NOT NULL,
    received_at     TEXT NOT NULL,
    batch_id        INTEGER NOT NULL,
    payload_hash    TEXT NOT NULL,
    data_json       TEXT NOT NULL,
    dup_of_hash     TEXT,
    dup_kind        TEXT NOT NULL,            -- first|retry|conflict
    late            INTEGER NOT NULL DEFAULT 0,
    UNIQUE(sounding_id, device, seq, payload_hash)
);

CREATE INDEX IF NOT EXISTS idx_raw_sounding ON raw_packets(sounding_id);
CREATE INDEX IF NOT EXISTS idx_raw_seq ON raw_packets(sounding_id, device, seq);

-- One row per ingest batch: batch acceptance and profile publication are
-- separate events. A failed publish never mutates prior published layers.
CREATE TABLE IF NOT EXISTS batches (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    sounding_id  TEXT NOT NULL REFERENCES soundings(id),
    accepted_at  TEXT NOT NULL,
    packet_count INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS versions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    sounding_id  TEXT NOT NULL REFERENCES soundings(id),
    kind         TEXT NOT NULL,               -- draft|candidate_a|candidate_b|published_snapshot
    parent_id    INTEGER REFERENCES versions(id),
    created_at   TEXT NOT NULL,
    batch_id     INTEGER NOT NULL,
    published_at TEXT,                        -- set once, by Publish only
    published_by TEXT,
    note         TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_ver_sounding ON versions(sounding_id);

CREATE TABLE IF NOT EXISTS version_points (
    version_id  INTEGER NOT NULL REFERENCES versions(id),
    device      TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    obs_time    TEXT NOT NULL,
    order_idx   INTEGER NOT NULL,
    branch_id   INTEGER NOT NULL,
    phase       TEXT NOT NULL,
    hash        TEXT NOT NULL,
    data_json   TEXT NOT NULL,
    flags_json  TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_vp_version ON version_points(version_id, order_idx);

CREATE TABLE IF NOT EXISTS branches (
    version_id  INTEGER NOT NULL,
    branch_id   INTEGER NOT NULL,
    phase       TEXT NOT NULL,
    start_time  TEXT NOT NULL,
    end_time    TEXT NOT NULL,
    source      TEXT NOT NULL,               -- auto|manual
    PRIMARY KEY(version_id, branch_id)
);

CREATE TABLE IF NOT EXISTS findings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id  INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    start_time  TEXT NOT NULL,
    end_time    TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    evidence    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_find_version ON findings(version_id);

CREATE TABLE IF NOT EXISTS layers (
    version_id   INTEGER NOT NULL,
    branch_id    INTEGER NOT NULL,
    phase        TEXT NOT NULL,
    pressure     REAL NOT NULL,
    obs_time     TEXT,
    temp         REAL,
    rh           REAL,
    alt_gps      REAL,
    lat          REAL,
    lon          REAL,
    exact        INTEGER NOT NULL DEFAULT 0,
    interpolated INTEGER NOT NULL DEFAULT 0,
    flags_json   TEXT NOT NULL DEFAULT '[]',
    missing      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(version_id, branch_id, pressure)
);
CREATE INDEX IF NOT EXISTS idx_layers_version ON layers(version_id);

-- Published layers are copied into a frozen table so that any later draft
-- work (including a fresh re-run) is physically incapable of touching a
-- signed vertical profile.
CREATE TABLE IF NOT EXISTS published_layers (
    publish_id  INTEGER NOT NULL,
    version_id  INTEGER NOT NULL,
    sounding_id TEXT NOT NULL,
    branch_id   INTEGER NOT NULL,
    phase       TEXT NOT NULL,
    pressure    REAL NOT NULL,
    obs_time    TEXT,
    temp        REAL,
    rh          REAL,
    alt_gps     REAL,
    lat         REAL,
    lon         REAL,
    exact       INTEGER NOT NULL,
    interpolated INTEGER NOT NULL,
    flags_json  TEXT NOT NULL,
    missing     TEXT NOT NULL,
    PRIMARY KEY(publish_id, branch_id, pressure)
);

CREATE TABLE IF NOT EXISTS publishes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    sounding_id  TEXT NOT NULL REFERENCES soundings(id),
    version_id   INTEGER NOT NULL,
    published_at TEXT NOT NULL,
    published_by TEXT NOT NULL,
    layer_count  INTEGER NOT NULL,
    note         TEXT NOT NULL DEFAULT ''
);

-- Human judgments. rev implements optimistic concurrency: two analysts
-- editing the same interval concurrently produce a 409 identifying the
-- affected time range.
CREATE TABLE IF NOT EXISTS judgments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    sounding_id   TEXT NOT NULL REFERENCES soundings(id),
    kind          TEXT NOT NULL,
    start_time    TEXT,
    end_time      TEXT,
    phase         TEXT NOT NULL DEFAULT '',
    device        TEXT NOT NULL DEFAULT '',
    seq           INTEGER,
    chosen_hash   TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT '',
    signed_by     TEXT NOT NULL,
    rev           INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL,
    superseded_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_judge_sounding ON judgments(sounding_id);
`
