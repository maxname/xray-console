-- When a message that could not be delivered is tried next. Without it every
-- round was an attempt, and a channel that was down for a few minutes had
-- used all of them up.
ALTER TABLE alert_outbox ADD COLUMN retry_at INTEGER NOT NULL DEFAULT 0;
