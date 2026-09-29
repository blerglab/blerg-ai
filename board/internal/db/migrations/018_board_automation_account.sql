-- Whose automation token a board holds: the blerg-core account the token acts
-- for (its on_behalf_of), recorded when it was saved. Every session the board
-- starts runs on that person's own engine credential, and anyone who can write
-- cards on the board can cause it to be spent — so the board shows it.
-- NULL when no token is set.
ALTER TABLE boards ADD COLUMN automation_account_id text;
