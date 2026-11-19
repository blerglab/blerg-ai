-- Explicit urgency requires the person to answer and bypasses quiet hours. It grants no approval.
ALTER TABLE messages ADD COLUMN urgent boolean NOT NULL DEFAULT false;

-- Quiet-hour pushes survive restarts. One latest notice per account/target batches
-- overnight activity; revision fencing protects a newer notice from an old drain.
CREATE TABLE held_pushes (
 account_id text NOT NULL,
 target text NOT NULL,
 title text NOT NULL,
 body text NOT NULL,
 message_id uuid REFERENCES messages(id) ON DELETE CASCADE,
 revision bigint NOT NULL DEFAULT 1,
 notice_count bigint NOT NULL DEFAULT 1,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY(account_id,target)
);
CREATE INDEX held_pushes_ready ON held_pushes(lease_until,account_id,target);
