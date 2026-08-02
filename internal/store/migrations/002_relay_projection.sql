ALTER TABLE target_desired_snapshots
    ADD COLUMN relay_failure_count integer NOT NULL DEFAULT 0 CHECK (relay_failure_count >= 0),
    ADD COLUMN relay_next_retry_at timestamptz,
    ADD COLUMN relay_suspended boolean NOT NULL DEFAULT false,
    ADD COLUMN relay_last_member_check_at timestamptz,
    ADD COLUMN relay_member_status jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(relay_member_status) = 'array');

CREATE INDEX target_desired_snapshots_relay_retry_idx
    ON target_desired_snapshots(relay_next_retry_at)
    WHERE relay_next_retry_at IS NOT NULL AND NOT relay_suspended;
