-- A job may belong to a device instead of a server: setting a device up and
-- sending it settings become jobs like the others, with steps, a log, a retry
-- from the step that failed.
--
-- SQLite cannot drop NOT NULL from a column, so the table is made anew.
-- Dropping `operations` as it is would delete every log line with it — the
-- log refers to it and deletes follow — so the old table is first renamed
-- (the log's reference follows the name), the log is copied to a table that
-- refers to the new one, and only then do the old two go.
ALTER TABLE operations RENAME TO operations_old;

CREATE TABLE operations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Exactly one of the two: whose job this is.
    server_id   INTEGER REFERENCES servers (id) ON DELETE CASCADE,
    device_id   INTEGER REFERENCES devices (id) ON DELETE CASCADE,
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
    finished_at INTEGER,
    CHECK ((server_id IS NULL) != (device_id IS NULL))
);
INSERT INTO operations (id, server_id, kind, status, steps, error, params, result, started_at, finished_at)
    SELECT id, server_id, kind, status, steps, error, params, result, started_at, finished_at
    FROM operations_old;

CREATE TABLE operation_log_new (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id INTEGER NOT NULL REFERENCES operations (id) ON DELETE CASCADE,
    at           INTEGER NOT NULL,
    step         TEXT    NOT NULL,
    -- cmd | out | info | error
    kind         TEXT    NOT NULL,
    text         TEXT    NOT NULL
);
INSERT INTO operation_log_new (seq, operation_id, at, step, kind, text)
    SELECT seq, operation_id, at, step, kind, text FROM operation_log;

DROP TABLE operation_log;
DROP TABLE operations_old;
ALTER TABLE operation_log_new RENAME TO operation_log;

CREATE INDEX operations_server ON operations (server_id, id);
CREATE INDEX operations_device ON operations (device_id, id);
CREATE INDEX operation_log_operation ON operation_log (operation_id, seq);

-- What a device's job is doing, why it stopped and what it printed are the
-- job's own from now on; these copies in the device's row are not written
-- any more. They stay as columns: SQLite of the age some servers have cannot
-- drop one, and nothing reads them.
UPDATE devices SET step = '', failure = NULL, log = '';
