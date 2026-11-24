-- Traffic, as Xray on each server counts it. The panel reads the counters
-- every few minutes and keeps the differences by the hour.
--
-- scope is what was counted: 'inbound' — everything that came in through the
-- server's inbound; 'user' — one user of it (a subscription, a chain, the
-- panel's own checks), named by its email.
CREATE TABLE traffic_hourly (
    server_id INTEGER NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- Unix time divided by 3600.
    hour      INTEGER NOT NULL,
    scope     TEXT    NOT NULL,
    name      TEXT    NOT NULL,
    -- Bytes from clients to the server, and from the server to clients.
    up        INTEGER NOT NULL DEFAULT 0,
    down      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (server_id, hour, scope, name)
);
CREATE INDEX traffic_hourly_hour ON traffic_hourly (hour);

-- The counters as last read, to tell how much was added since. Xray's
-- counters start from zero when it restarts.
CREATE TABLE traffic_counters (
    server_id INTEGER NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    scope     TEXT    NOT NULL,
    name      TEXT    NOT NULL,
    up        INTEGER NOT NULL DEFAULT 0,
    down      INTEGER NOT NULL DEFAULT 0,
    at        INTEGER NOT NULL,
    PRIMARY KEY (server_id, scope, name)
);

-- What the administrator has been, or is yet to be, told about.
--
-- One row per outage that crossed the "tell me after" threshold: it is what
-- makes "the server is back" follow only an outage that was reported, and
-- what repeats count from.
CREATE TABLE alert_incidents (
    server_id   INTEGER PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    -- When the outage began; a different value means a different outage.
    down_since  INTEGER NOT NULL,
    notified_at INTEGER NOT NULL
);

-- Messages waiting to be delivered — at once, or when quiet hours end.
CREATE TABLE alert_outbox (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL,
    -- server_down | server_up | update_available | test
    kind       TEXT    NOT NULL,
    server     TEXT    NOT NULL DEFAULT '',
    -- JSON facts of the event, as a webhook receives them.
    data       TEXT    NOT NULL DEFAULT '{}',
    -- The sentence a person reads.
    text       TEXT    NOT NULL,
    -- Which channels already have it: a channel that works is not sent the
    -- same message again while another is retried.
    sent_telegram INTEGER NOT NULL DEFAULT 0,
    sent_webhook  INTEGER NOT NULL DEFAULT 0,
    -- How many delivery rounds failed so far; a message is dropped after a few.
    attempts   INTEGER NOT NULL DEFAULT 0
);
