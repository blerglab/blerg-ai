-- 072: questions that do not block (0.4.4). `blerg-runner ask --no-wait` posts an ask the agent does
-- not wait on: it stays in the session's Needs you until the person answers it there (the answer is
-- then sent to the agent as a message tied to the question) or the agent marks it answered. A daemon
-- restart does not close it, and a chat message does not answer it.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS no_wait boolean NOT NULL DEFAULT false;
