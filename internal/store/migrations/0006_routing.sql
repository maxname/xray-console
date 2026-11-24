-- A rule set decides, for the clients it is given to, which traffic goes into
-- which chain, which goes straight out and which is blocked. The rules run on
-- one server — the entry those clients connect to.
CREATE TABLE rule_sets (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL UNIQUE,
    entry_server_id INTEGER NOT NULL REFERENCES servers (id),
    -- JSON list of rules, in the order they are tried.
    rules           TEXT    NOT NULL DEFAULT '[]',
    -- Where everything no rule claimed goes: chain | direct | block.
    default_action  TEXT    NOT NULL DEFAULT 'direct',
    default_chain_id INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- A subscription is one person's access: a user on the entry server of its
-- rule set, and a link an app fetches the connection settings from.
CREATE TABLE subscriptions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    rule_set_id INTEGER NOT NULL REFERENCES rule_sets (id),
    -- SHA-256 of the link's token, for finding the row; the token itself is
    -- kept sealed so that it can be shown again.
    token_hash  BLOB    NOT NULL UNIQUE,
    token       BLOB    NOT NULL,
    -- The user's id on the server is derived from this; sealed.
    secret      BLOB    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    -- NULL: no end date.
    expires_at  INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    -- When an app last fetched the link; NULL if never.
    fetched_at  INTEGER
);
