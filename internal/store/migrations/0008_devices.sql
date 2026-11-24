-- A device is a machine on the administrator's side — a router, a computer —
-- on which the panel installs an Xray client over SSH. To the servers it is
-- what a subscription is: a user on the entry server of its rule set.
CREATE TABLE devices (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    host        TEXT    NOT NULL,
    ssh_port    INTEGER NOT NULL,
    ssh_user    TEXT    NOT NULL,
    -- The device's SSH host key, pinned when it was added.
    host_key    TEXT    NOT NULL,
    rule_set_id INTEGER NOT NULL REFERENCES rule_sets (id),
    -- The user's id on the entry server is derived from this; sealed.
    secret      BLOB    NOT NULL,
    -- The local proxies the client offers on the device.
    socks_port  INTEGER NOT NULL,
    http_port   INTEGER NOT NULL,
    -- 1: the proxies listen on every interface, for the device's network.
    lan         INTEGER NOT NULL DEFAULT 0,
    -- 1: changed settings are sent to the device without being asked.
    auto_push   INTEGER NOT NULL DEFAULT 1,
    -- new | working | ready | failed
    status      TEXT    NOT NULL DEFAULT 'new',
    -- The step a job is on, or stopped at.
    step        TEXT    NOT NULL DEFAULT '',
    os_name     TEXT    NOT NULL DEFAULT '',
    arch        TEXT    NOT NULL DEFAULT '',
    xray_version TEXT   NOT NULL DEFAULT '',
    -- 1: the panel put its key in authorized_keys, and takes it out again
    -- when the device is deleted.
    key_added   INTEGER NOT NULL DEFAULT 0,
    -- SHA-256 of the configuration last put on the device; '' before the first.
    applied     TEXT    NOT NULL DEFAULT '',
    -- JSON {code, message, details} of the last job if it failed.
    failure     TEXT,
    -- What the last job did, line by line.
    log         TEXT    NOT NULL DEFAULT '',
    -- What the last look at the device found.
    online      INTEGER NOT NULL DEFAULT 0,
    service     TEXT    NOT NULL DEFAULT '',
    seen_at     INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
