ALTER TABLE scopes ADD COLUMN broker_next_attempt_at TIMESTAMP;

-- Worker poll ordering index. Partial to keep it tiny: only rows the
-- worker might act on (pending or revoking). Ordered by
-- broker_next_attempt_at so SELECT ... LIMIT N returns due rows first.
CREATE INDEX scopes_broker_ready_idx
    ON scopes(broker_next_attempt_at)
    WHERE broker_status IN ('pending', 'revoking');
