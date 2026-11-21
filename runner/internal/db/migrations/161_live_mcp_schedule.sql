-- Inert while the reviewed polling gate is false. Authority stays in Core.
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS workspace_id uuid REFERENCES sessions(id) ON DELETE CASCADE;
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS next_due_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS expires_at timestamptz;
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS failure_count integer NOT NULL DEFAULT 0;
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS cause text NOT NULL DEFAULT '';
ALTER TABLE live_mcp_binding_state ADD COLUMN IF NOT EXISTS sample_sequence bigint NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS live_mcp_due ON live_mcp_binding_state(next_due_at) WHERE enabled;
ALTER TABLE live_mcp_attempts ADD COLUMN IF NOT EXISTS listing_credential_at timestamptz;
ALTER TABLE live_mcp_attempts ADD COLUMN IF NOT EXISTS call_credential_at timestamptz;
-- Refused attempts have a completed audit without pretending a dispatch occurred.
ALTER TABLE live_mcp_attempts DROP CONSTRAINT IF EXISTS live_mcp_attempts_outcome_check;
ALTER TABLE live_mcp_attempts DROP CONSTRAINT IF EXISTS live_mcp_attempts_check1;
ALTER TABLE live_mcp_attempts DROP CONSTRAINT IF EXISTS live_mcp_attempts_check2;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='live_mcp_attempts'::regclass AND conname='live_mcp_attempts_outcomes') THEN
 ALTER TABLE live_mcp_attempts ADD CONSTRAINT live_mcp_attempts_outcomes CHECK(outcome IN ('undispatched','dispatched','ok','failed','interrupted','refused'));
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='live_mcp_attempts'::regclass AND conname='live_mcp_attempts_dispatch_state') THEN
 ALTER TABLE live_mcp_attempts ADD CONSTRAINT live_mcp_attempts_dispatch_state CHECK((outcome IN ('undispatched','refused'))=(dispatched_at IS NULL));
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='live_mcp_attempts'::regclass AND conname='live_mcp_attempts_finish_state') THEN
 ALTER TABLE live_mcp_attempts ADD CONSTRAINT live_mcp_attempts_finish_state CHECK(finished_at IS NULL OR dispatched_at IS NOT NULL OR outcome='refused');
 END IF;
END $$;
ALTER TABLE live_mcp_attempts ADD COLUMN IF NOT EXISTS refusal_cause text NOT NULL DEFAULT '';
ALTER TABLE live_dataset_attempts DROP CONSTRAINT IF EXISTS live_dataset_attempts_source_check;
ALTER TABLE live_dataset_attempts ADD CONSTRAINT live_dataset_attempts_source_check CHECK(source IN (
 'blerg.workers','blerg.engine.usage','blerg.cards.merged','blerg.gates.durations',
 'blerg.gates.bins','blerg.cards.merged.heatmap','blerg.workers.timeline',
 'blerg.cards.time','blerg.reviews.pairs','blerg.mcp'));
