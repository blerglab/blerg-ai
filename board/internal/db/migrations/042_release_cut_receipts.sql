-- Evidence is written by the guarded close, never reconstructed from a later board.
CREATE TABLE release_cut_receipts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 board_id uuid NOT NULL,
 release_id uuid NOT NULL UNIQUE,
 ask_id uuid UNIQUE,
 receipt jsonb NOT NULL
);
CREATE FUNCTION release_cut_receipt_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'release cut receipt is immutable'; END $$;
CREATE TRIGGER release_cut_receipt_immutable BEFORE UPDATE OR DELETE ON release_cut_receipts FOR EACH ROW EXECUTE FUNCTION release_cut_receipt_immutable();
