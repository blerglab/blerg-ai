CREATE TABLE IF NOT EXISTS live_mcp_decision_outbox (
 ask_id uuid PRIMARY KEY,
 body jsonb NOT NULL,
 operation text NOT NULL CHECK(operation IN ('confirm','cancel')),
 delivered boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION live_mcp_decision_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.subject->>'kind'='live_binding' AND NEW.status<>OLD.status AND NEW.status IN ('committed','declined','canceled') THEN
  INSERT INTO live_mcp_decision_outbox(ask_id,body,operation)
  VALUES(NEW.message_id,jsonb_build_object('account_id',NEW.owner_id,'binding_id',NEW.subject->'live_binding'->'scope'->>'binding','revision',NEW.subject->'live_binding'->'scope'->'revision','digest',NEW.subject->>'digest','ask_id',NEW.message_id),CASE WHEN NEW.status='committed' THEN 'confirm' ELSE 'cancel' END)
  ON CONFLICT(ask_id) DO UPDATE SET operation=CASE WHEN EXCLUDED.operation='cancel' THEN 'cancel' ELSE live_mcp_decision_outbox.operation END,delivered=false;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS live_mcp_decision_enqueue ON bound_asks;
CREATE TRIGGER live_mcp_decision_enqueue AFTER UPDATE ON bound_asks FOR EACH ROW EXECUTE FUNCTION live_mcp_decision_enqueue();

ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS last_view_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS last_payload jsonb;
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS payload_at timestamptz;
