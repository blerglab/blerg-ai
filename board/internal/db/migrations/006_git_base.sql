-- Per-board git base URL: not every board's repos live under the runner's
-- default org. Empty = use the runner's RUNNER_GIT_BASE.
ALTER TABLE boards ADD COLUMN git_base text NOT NULL DEFAULT '';
