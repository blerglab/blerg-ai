ALTER TABLE messages ADD COLUMN question_turn text NOT NULL DEFAULT 'person' CHECK (question_turn IN ('person','main'));
CREATE TABLE question_thread_entries (
 seq bigserial PRIMARY KEY,
 message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 client_id text NOT NULL,
 author text NOT NULL CHECK (author IN ('person','main')),
 kind text NOT NULL CHECK (kind IN ('reply','revision')),
 body text NOT NULL,
 previous_body text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(message_id, client_id)
);
CREATE INDEX question_thread_message_seq ON question_thread_entries(message_id,seq);
