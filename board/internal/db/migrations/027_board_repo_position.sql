-- A board's repos keep the order they were given: the first is the working directory of board
-- sessions and board-level discovery. Until now they were read alphabetically, so renaming one
-- (a folder name resolved to owner/name) could promote another. Existing rows keep the
-- alphabetical order they were read in.
ALTER TABLE board_repos ADD COLUMN IF NOT EXISTS position integer NOT NULL DEFAULT 0;
UPDATE board_repos r SET position = o.n
  FROM (SELECT board_id, repo, row_number() OVER (PARTITION BY board_id ORDER BY repo) - 1 AS n FROM board_repos) o
 WHERE r.board_id = o.board_id AND r.repo = o.repo;
