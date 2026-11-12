CREATE TABLE board_metadata_intents (
 intent uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 board_id uuid NOT NULL,
 digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
