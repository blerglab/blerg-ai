-- Sensitive reproduction text is deliberately absent from cards.body and its search vector.
ALTER TABLE boards ADD COLUMN include_component_version boolean NOT NULL DEFAULT false;
ALTER TABLE cards ADD COLUMN sensitive boolean NOT NULL DEFAULT false;
ALTER TABLE cards ADD COLUMN origin jsonb;
ALTER TABLE cards ADD CONSTRAINT sensitive_body_is_private CHECK (NOT sensitive OR body IS NULL);
CREATE TABLE sensitive_card_details (
 card_id uuid PRIMARY KEY REFERENCES cards(id) ON DELETE CASCADE,
 body text NOT NULL
);
-- Consumption survives card/board deletion: an approved intent can never be reused or retargeted.
CREATE TABLE filed_card_intents (
 intent uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 board_id uuid NOT NULL,
 digest text NOT NULL CHECK(digest ~ '^[0-9a-f]{64}$'),
 card_id uuid,
 number integer,
 created_at timestamptz NOT NULL DEFAULT now()
);
