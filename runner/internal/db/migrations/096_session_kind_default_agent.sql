-- Terminal (tmux) sessions were removed in 0.5.0: a new session row is an agent session from its
-- first write, so no reader ever sees a starting agent session as a terminal one.
ALTER TABLE sessions ALTER COLUMN kind SET DEFAULT 'agent';
