-- A board a person approved in Runner (a session proposed it) is created through one Core-signed
-- capability. Its intent is consumed in the same transaction that writes the board, and the
-- consumption outlives the board: an approved intent can never create a second board or another one.
CREATE TABLE board_create_intents (
 intent uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 digest text NOT NULL CHECK(digest ~ '^[0-9a-f]{64}$'),
 board_id uuid,
 created_at timestamptz NOT NULL DEFAULT now()
);
