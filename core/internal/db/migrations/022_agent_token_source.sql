-- Where an agent token came from, for the owner's Agent tokens list. '' is a token the person
-- created in Settings; 'board <id>' is the automation token a board minted for them with "Run as
-- me" (POST /internal/tokens/board-automation). The board key can revoke only tokens whose source
-- names that board, so this column is also what bounds that key.
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT '';
