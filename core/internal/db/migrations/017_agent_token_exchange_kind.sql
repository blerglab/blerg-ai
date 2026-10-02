-- Agent token kind 'exchange': the short-lived (10 minute) token core signs for a built-in target
-- on the runner's behalf (POST /internal/tokens/exchange). The row exists so the token's sub can be
-- revoked and so log-out-everywhere marks it revoked; it is hidden from GET /api/tokens and does
-- NOT count toward the 50-live-token user cap (it has its own bound in the identity package).
ALTER TABLE agent_tokens DROP CONSTRAINT IF EXISTS agent_tokens_kind_check;
ALTER TABLE agent_tokens ADD CONSTRAINT agent_tokens_kind_check
    CHECK (kind IN ('token', 'cron', 'exchange'));
