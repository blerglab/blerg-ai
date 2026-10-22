-- 074: the person can dismiss what waits on them in Needs you (0.4.5, card #155). A review request
-- they answered in the chat instead closes as 'dismissed' (not approved, not changes requested), and
-- a question closes as dismissed (not answered): the agent is told, and may ask again.
ALTER TABLE review_requests DROP CONSTRAINT IF EXISTS review_requests_state_check;
ALTER TABLE review_requests ADD CONSTRAINT review_requests_state_check
  CHECK (state IN ('pending', 'approved', 'changes_requested', 'withdrawn', 'superseded', 'dismissed'));
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dismissed boolean NOT NULL DEFAULT false;
-- dismissed_by: the account that dismissed it, so a viewer reads "by you" only for their own.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dismissed_by text NOT NULL DEFAULT '';
