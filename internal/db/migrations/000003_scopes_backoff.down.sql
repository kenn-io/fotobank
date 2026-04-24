DROP INDEX IF EXISTS scopes_broker_ready_idx;
ALTER TABLE scopes DROP COLUMN broker_next_attempt_at;
