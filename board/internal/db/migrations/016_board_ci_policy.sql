-- ci_policy: what a board's auto-merge does when the repo reports NO CI at
-- all for a PR head (no check-runs, no commit statuses).
--
-- 'required' (the default, and the behaviour before this column existed):
--   a green machine check is mandatory, so a repo with no CI workflow never
--   auto-merges. Correct for repos that have CI, and a deadlock for repos
--   that do not — the card is flagged stuck and waits for a human forever.
--
-- 'if_present': check the checks that exist. Nothing reported means nothing
--   to fail, so an adversarially-approved card merges on the review alone.
--   Red and still-running are unchanged under both policies: this governs
--   ABSENCE, never a failure.
--
-- Deliberately no 'none'/'ignored' value. A board that merges over a red
-- check is a foot-gun with no use case here, and leaving it out means no
-- setting of this column can ever land a failing PR.
ALTER TABLE boards
    ADD COLUMN ci_policy text NOT NULL DEFAULT 'required'
        CHECK (ci_policy IN ('required', 'if_present'));
