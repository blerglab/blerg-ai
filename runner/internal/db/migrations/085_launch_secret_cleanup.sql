-- Invocation metadata only: never persist the Secret body or credential values.
-- No session FK: deleting a session must not erase an unresolved allocation.
CREATE TABLE launch_secret_cleanup (
 attempt uuid PRIMARY KEY,
 namespace text NOT NULL,
 metadata jsonb NOT NULL,
 ready_after timestamptz NOT NULL
);
CREATE INDEX launch_secret_cleanup_due ON launch_secret_cleanup (namespace, ready_after);
