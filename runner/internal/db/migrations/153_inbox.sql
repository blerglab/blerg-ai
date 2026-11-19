-- Inbox content and read state belong to one recipient account, across devices.
-- No session foreign key: deleting a conversation must not delete reading material.
CREATE TABLE inbox_items (
 id text PRIMARY KEY,
 account_id text NOT NULL,
 session_id text NOT NULL,
 -- NULL preserves non-idempotent posting for callers without a client ID.
 client_id text CHECK (length(client_id) BETWEEN 1 AND 128),
 subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 200),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 262144),
 kind text NOT NULL CHECK (kind IN ('summary','release','failure','review','important')),
 created_at timestamptz NOT NULL DEFAULT now(),
 is_read boolean NOT NULL DEFAULT false,
 CONSTRAINT inbox_invocation UNIQUE (account_id, session_id, client_id)
);
CREATE INDEX inbox_recipient_date ON inbox_items(account_id, created_at DESC, id DESC);
