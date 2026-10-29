-- Cleanup-only authority and a durable terminal-effects outbox for desktop v2.
ALTER TABLE local_workspace_authority ADD COLUMN stop_requested boolean NOT NULL DEFAULT false;
ALTER TABLE local_workspace_authority ADD COLUMN effects_done boolean NOT NULL DEFAULT false;
