ALTER TABLE messages ADD COLUMN question_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN question_session_posted boolean NOT NULL DEFAULT false;
UPDATE messages m SET question_revision=COALESCE((SELECT max(seq) FROM question_thread_entries e WHERE e.message_id=m.id AND e.kind='revision'),0);
