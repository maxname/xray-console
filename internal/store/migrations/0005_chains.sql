-- A chain is a route through several servers: the client connects to the
-- first link, and each link passes the traffic to the next.
CREATE TABLE chains (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    -- The chain's users on its servers are derived from this; sealed.
    secret      BLOB    NOT NULL,
    -- JSON list of links, in order from the client outward:
    -- [{"servers": [3], "strategy": "", "reserve": 0}, {"servers": [1, 2], ...}]
    hops        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    -- The last end-to-end check through the chain; NULL until the first one.
    check_at    INTEGER,
    check_ok    INTEGER,
    check_ms    INTEGER,
    check_error TEXT    NOT NULL DEFAULT ''
);
