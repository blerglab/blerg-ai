-- A receipt charges a logical delivery once, across retries and server restarts.
CREATE TABLE worker_wake_receipts (
 parent_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 delivery_key text NOT NULL,
 PRIMARY KEY(parent_id,delivery_key)
);
CREATE TABLE main_relay_budgets (
 parent_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 window_start timestamptz NOT NULL,
 wakes integer NOT NULL,
 chatter integer NOT NULL
);
CREATE TABLE channel_close_notices (
 child_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 delivered boolean NOT NULL DEFAULT false
);
