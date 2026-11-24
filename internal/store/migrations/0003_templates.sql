-- A template is the settings several servers share. Every change makes a new
-- version; servers always aim for the latest one.
CREATE TABLE templates (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE TABLE template_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id INTEGER NOT NULL REFERENCES templates (id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    -- JSON object: field key → value, e.g. {"xhttp.mode": "auto"}.
    "values"    TEXT    NOT NULL,
    -- JSON list of field keys a server may override.
    overridable TEXT    NOT NULL,
    -- What changed from the previous version: "xhttp.mode: packet-up → auto".
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    UNIQUE (template_id, version)
);

ALTER TABLE servers ADD COLUMN template_id INTEGER REFERENCES templates (id);
-- JSON object: field key → this server's own value.
ALTER TABLE servers ADD COLUMN overrides TEXT NOT NULL DEFAULT '{}';
-- What is on the server now, as of the last successful apply: the rendered
-- config.json and everything it was rendered from. Sealed — it holds the
-- REALITY private key. NULL until the first apply.
ALTER TABLE servers ADD COLUMN applied BLOB;
