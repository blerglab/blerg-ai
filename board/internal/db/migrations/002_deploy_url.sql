-- deploy_url: where this board's project runs, when it runs somewhere.
-- Rendered as a prominent LIVE link on the board and in review context.
ALTER TABLE boards ADD COLUMN deploy_url text;
