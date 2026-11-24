-- The certificate each server with its own domain shows a visitor, as the
-- panel last saw it from outside. One row per server; a server that
-- masquerades as someone else's site has none.
CREATE TABLE server_certs (
    server_id   INTEGER PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    domain      TEXT    NOT NULL,
    -- The certificate's own dates; 0 until one has been read.
    not_before  INTEGER NOT NULL DEFAULT 0,
    not_after   INTEGER NOT NULL DEFAULT 0,
    issuer      TEXT    NOT NULL DEFAULT '',
    checked_at  INTEGER NOT NULL,
    -- Why the last look did not find a certificate good for the domain: the
    -- server did not answer, or what it showed would not be accepted. '' when
    -- it did.
    error       TEXT    NOT NULL DEFAULT '',
    -- The not_after the administrator was told about as "not renewed", so
    -- that one certificate is one message.
    alerted_for INTEGER NOT NULL DEFAULT 0
);
