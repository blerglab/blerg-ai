-- Zero means no scoped runtime was activated. Lifecycle wiring follows separately.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS mcp_launch_generation bigint NOT NULL DEFAULT 0 CHECK(mcp_launch_generation>=0);
ALTER TABLE session_mcp_grants ADD COLUMN IF NOT EXISTS session_scope jsonb;
ALTER TABLE mcp_proposals ADD COLUMN IF NOT EXISTS session_scope jsonb;
ALTER TABLE session_mcp_grants DROP CONSTRAINT IF EXISTS mcp_grant_session_scope;
ALTER TABLE session_mcp_grants ADD CONSTRAINT mcp_grant_session_scope CHECK (
 session_scope IS NULL OR (
 jsonb_typeof(session_scope)='object' AND builtin='' AND proof_kind='session_id'
 AND session_scope->>'account_id' IS NOT NULL AND session_scope->>'account_id'=account_id
 AND session_scope->>'runner_session_id' IS NOT NULL AND session_scope->>'runner_session_id'=session_id::text
 AND session_scope->>'human_session_id' IS NOT NULL AND session_scope->>'human_session_id'=proof_value
 AND session_scope->>'runner_installation_id' IS NOT NULL AND length(session_scope->>'runner_installation_id') BETWEEN 1 AND 128
 AND session_scope->>'launch_generation' IS NOT NULL AND (session_scope->>'launch_generation')::bigint>0
 AND session_scope->>'credential_generation' IS NOT NULL AND (session_scope->>'credential_generation')::bigint>0
 AND session_scope->>'grant_token_hash' IS NOT NULL AND length(session_scope->>'grant_token_hash')=64
 AND decode(session_scope->>'grant_token_hash','hex')=token_hash
 ));
ALTER TABLE mcp_proposals DROP CONSTRAINT IF EXISTS mcp_proposal_session_scope;
ALTER TABLE mcp_proposals ADD CONSTRAINT mcp_proposal_session_scope CHECK (
 session_scope IS NULL OR (
 jsonb_typeof(session_scope)='object' AND session_id IS NOT NULL AND cron_id IS NULL
 AND session_scope->>'account_id' IS NOT NULL AND session_scope->>'account_id'=account_id
 AND session_scope->>'runner_session_id' IS NOT NULL AND session_scope->>'runner_session_id'=session_id::text
 AND session_scope->>'human_session_id' IS NOT NULL AND length(session_scope->>'human_session_id')=36
 AND session_scope->>'runner_installation_id' IS NOT NULL AND length(session_scope->>'runner_installation_id') BETWEEN 1 AND 128
 AND session_scope->>'launch_generation' IS NOT NULL AND (session_scope->>'launch_generation')::bigint>0
 AND session_scope->>'credential_generation' IS NOT NULL AND (session_scope->>'credential_generation')::bigint>0
 AND session_scope->>'grant_token_hash' IS NOT NULL AND length(session_scope->>'grant_token_hash')=64
 AND octet_length(decode(session_scope->>'grant_token_hash','hex'))=32
 ));
