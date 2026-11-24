-- Where each server stands with the availability checks right now.
CREATE TABLE probe_state (
    server_id    INTEGER PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    -- unknown | up | down
    state        TEXT    NOT NULL DEFAULT 'unknown',
    -- When the server entered this state. For "down" that is the first failed
    -- check of the streak, not the moment the threshold was crossed.
    since        INTEGER NOT NULL,
    -- Consecutive results of each kind; one resets the other.
    fails        INTEGER NOT NULL DEFAULT 0,
    successes    INTEGER NOT NULL DEFAULT 0,
    -- When the current run of failures began; NULL while the last check passed.
    failing_since INTEGER,
    last_at      INTEGER,
    last_ok      INTEGER,
    last_tcp_ms  INTEGER,
    last_request_ms INTEGER,
    -- Which part of the last check failed: tcp | request | ''.
    last_stage   TEXT    NOT NULL DEFAULT '',
    last_error   TEXT    NOT NULL DEFAULT ''
);

-- Every check, kept for 90 days.
CREATE TABLE probe_results (
    id         INTEGER PRIMARY KEY,
    server_id  INTEGER NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    ok         INTEGER NOT NULL,
    tcp_ms     INTEGER,
    request_ms INTEGER,
    stage      TEXT    NOT NULL DEFAULT '',
    error      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX probe_results_server_at ON probe_results (server_id, at);

-- Per-day totals, kept for good and updated with every check, so "the last 30
-- days" is thirty rows rather than forty thousand.
CREATE TABLE probe_daily (
    server_id  INTEGER NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- Local date of the panel, YYYY-MM-DD.
    day        TEXT    NOT NULL,
    total      INTEGER NOT NULL DEFAULT 0,
    ok         INTEGER NOT NULL DEFAULT 0,
    -- Sum of tcp_ms over successful checks, for a daily mean.
    tcp_ms_sum INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, day)
);

-- What happened, for the overview's feed. The server's name is copied in: the
-- feed must still read sensibly after the server is gone.
CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    -- server_installed | server_removed | config_applied | apply_failed |
    -- server_down | server_up
    kind        TEXT    NOT NULL,
    server_id   INTEGER REFERENCES servers (id) ON DELETE SET NULL,
    server_name TEXT    NOT NULL DEFAULT '',
    data        TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX events_at ON events (at);
