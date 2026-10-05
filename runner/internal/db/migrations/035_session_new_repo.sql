-- sessions.new_repo: the session was launched as "New repository" on the cluster. The server
-- created the repository on the person's git provider (or could not, and the pod then initialises
-- an empty one pointed at where it would be). Recorded so a resume rebuilds the same Job: the pod
-- is told BLERG_RUNNER_NEW_REPO=1 again and the operator's shared git token is withheld again.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS new_repo boolean NOT NULL DEFAULT false;
