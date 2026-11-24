-- Panel-wide settings the administrator can change: panel_url, setup_token, …
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- The panel has exactly one administrator; the CHECK keeps it that way.
CREATE TABLE admin (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    username            TEXT    NOT NULL,
    password_hash       TEXT    NOT NULL,
    -- Sealed with the master key. Present but not enabled while the
    -- administrator has been shown the secret and has not confirmed a code yet.
    totp_secret         BLOB,
    totp_enabled        INTEGER NOT NULL DEFAULT 0,
    -- Last accepted time step: a code is good once, even inside its window.
    totp_last_step      INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL,
    last_login_at       INTEGER,
    last_login_ip       TEXT
);

CREATE TABLE sessions (
    -- SHA-256 of the cookie value: a copy of the database is not a set of
    -- working sessions.
    token_hash   BLOB    PRIMARY KEY,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    remember     INTEGER NOT NULL,
    ip           TEXT    NOT NULL,
    user_agent   TEXT    NOT NULL
);

-- Failed sign-ins only; a successful one clears the address's rows.
CREATE TABLE login_failures (
    id INTEGER PRIMARY KEY,
    ip TEXT    NOT NULL,
    at INTEGER NOT NULL
);
CREATE INDEX login_failures_ip_at ON login_failures (ip, at);
