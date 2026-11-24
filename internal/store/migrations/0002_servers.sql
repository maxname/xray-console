-- SSH keys the panel signs in with. The first one, 'panel-ed25519', is created
-- on demand.
CREATE TABLE ssh_keys (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    algorithm   TEXT    NOT NULL,
    -- authorized_keys line, comment included.
    public_key  TEXT    NOT NULL,
    fingerprint TEXT    NOT NULL,
    -- OpenSSH private key, sealed with the master key.
    private_key BLOB    NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE servers (
    -- AUTOINCREMENT: an id is never reused, so a link to a deleted server
    -- cannot quietly start showing a different one.
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    location    TEXT    NOT NULL DEFAULT '',
    host        TEXT    NOT NULL,
    ssh_port    INTEGER NOT NULL DEFAULT 22,
    ssh_user    TEXT    NOT NULL DEFAULT 'root',
    -- The host key the administrator confirmed, "type base64". The panel
    -- refuses to connect to anything else.
    host_key    TEXT    NOT NULL,
    -- new | installing | failed | ready
    status      TEXT    NOT NULL DEFAULT 'new',
    os_name     TEXT    NOT NULL DEFAULT '',
    arch        TEXT    NOT NULL DEFAULT '',
    xray_version TEXT   NOT NULL DEFAULT '',
    -- The VLESS + REALITY + XHTTP inbound, JSON sealed with the master key:
    -- it holds the REALITY private key.
    inbound     BLOB,
    -- JSON list of what the panel changed on the server, kept so that
    -- uninstalling can put everything back.
    manifest    TEXT    NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- A long-running job against one server (an installation, later an update or
-- an uninstall). Steps are a JSON list so a new kind needs no schema change.
CREATE TABLE operations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   INTEGER NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL,
    -- running | succeeded | failed | rolled_back
    status      TEXT    NOT NULL,
    steps       TEXT    NOT NULL,
    -- JSON {code, step, message, details} when status is failed.
    error       TEXT,
    -- JSON of what the caller asked for, minus anything secret.
    params      TEXT    NOT NULL DEFAULT '{}',
    -- JSON facts the job found out, shown when it is done.
    result      TEXT    NOT NULL DEFAULT '{}',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE INDEX operations_server ON operations (server_id, id);

CREATE TABLE operation_log (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id INTEGER NOT NULL REFERENCES operations (id) ON DELETE CASCADE,
    at           INTEGER NOT NULL,
    step         TEXT    NOT NULL,
    -- cmd | out | info | error
    kind         TEXT    NOT NULL,
    text         TEXT    NOT NULL
);
CREATE INDEX operation_log_operation ON operation_log (operation_id, seq);
